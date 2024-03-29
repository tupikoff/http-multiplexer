package main

import (
	"encoding/json"
	"errors"
	"golang.org/x/net/context"
	"golang.org/x/net/netutil"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// MaxIncomingUrls :: number of max incoming urls
const MaxIncomingUrls = 20

// MaxOutgoingRequests - Maximum number of simultaneous outgoing requests (goroutines) on each incoming
const MaxOutgoingRequests = 4

// ClientTimeoutSec - Outgoing request timeout
const ClientTimeoutSec = 1

// MaxConnections - Maximum incoming connections
const MaxConnections = 100

// ServerShutdownTimeoutSec :: timeout for wait for shutdown gracefully
const ServerShutdownTimeoutSec = 30

// InputData :: struct for data in incoming request
type InputData struct {
	Urls []string
}

// Data :: struct for result
type Data struct {
	Url  string `json:"url"`
	Body string `json:"body"`
}

// Result :: Internal storage of results with key as order of url in incoming sequence
type Result struct {
	mx   sync.RWMutex
	Maps map[int]Data
}

// OutputData :: struct for outgoing response
type OutputData struct {
	Pages []Data `json:"pages"`
}

func handler(w http.ResponseWriter, r *http.Request) {
	// Read incoming data into struct
	var inputData InputData
	err := json.NewDecoder(r.Body).Decode(&inputData)
	if err != nil {
		outputError(w, err)
		return
	}
	log.Printf("%v", inputData)

	// Reject request with more than twenty urls
	if len(inputData.Urls) > MaxIncomingUrls {
		outputError(w, errors.New("more than 20 urls"))
		return
	}

	var result Result
	result.Maps = make(map[int]Data, len(inputData.Urls))
	var wg sync.WaitGroup
	// Use the buffered channel of structs as semaphore
	semaphoreChan := make(chan struct{}, MaxOutgoingRequests)
	errorChan := make(chan error, 1)
	ctx := r.Context()
	num := 0

	for _, url := range inputData.Urls {
		// Wait here in case of more than MaxOutgoingRequests running
		semaphoreChan <- struct{}{}
		wg.Add(1)

		go func(url string, num int, result *Result, wg *sync.WaitGroup) {
			defer wg.Done()
			page, err := fetchPageContent(url)
			if err != nil {
				errorChan <- err
				return
			}
			// Fill the result map with prevent race conditions
			result.mx.RLock()
			result.Maps[num] = Data{Url: url, Body: page}
			result.mx.RUnlock()

			<-semaphoreChan
		}(url, num, &result, &wg)
		num++

		// Wait for all goroutines done
		if num == len(inputData.Urls) {
			wg.Wait()
		}

		// Handle errors and user cancellation
		select {
		case err := <-errorChan:
			outputError(w, err)
			return
		case <-ctx.Done():
			log.Println("Request was cancelled")
			return
		default:
		}
	}

	// Compile output data in incoming order
	var outputData OutputData
	for i := 0; i < num; i++ {
		outputData.Pages = append(outputData.Pages, result.Maps[i])
	}

	w.WriteHeader(http.StatusOK)
	err = json.NewEncoder(w).Encode(&outputData)
	if err != nil {
		outputError(w, err)
		return
	}
}

func fetchPageContent(url string) (page string, err error) {
	client := http.Client{Timeout: ClientTimeoutSec * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return
	}
	defer response.Body.Close()
	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return
	}
	page = string(body)
	return
}

func outputError(responseWriter http.ResponseWriter, err error) {
	log.Println(err)
	http.Error(responseWriter, err.Error(), http.StatusBadRequest)
}

func main() {
	router := http.NewServeMux()
	router.HandleFunc("/", handler)
	server := http.Server{
		Handler: router,
	}

	// Limit incoming connections
	listener, err := net.Listen("tcp", ":9999")
	if err != nil {
		log.Fatal(err)
	}
	listener = netutil.LimitListener(listener, MaxConnections)
	defer listener.Close()
	log.Printf("Server listening on %s\n", listener.Addr().String())

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	// Handle terminations signals
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan
	log.Println("Shutting down the server gracefully...")

	// Shutdown gracefully
	ctx, cancel := context.WithTimeout(context.Background(), ServerShutdownTimeoutSec*time.Second)
	defer cancel()
	server.SetKeepAlivesEnabled(false)
	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("%v", err)
	}
}
