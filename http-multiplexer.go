package main

import (
	"encoding/json"
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

const MaxGoroutines = 4
const ClientTimeoutSec = 1
const MaxConnections = 200
const ServerShutdownTimeoutSec = 30

type InputData struct {
	Urls []string
}

type Data struct {
	Url  string `json:"url"`
	Body string `json:"body"`
}

// Internal storage of results with key as order of url in incoming sequence
type Result struct {
	mx   sync.RWMutex
	Maps map[int]Data
}

type OutputData struct {
	Pages []Data `json:"pages"`
}

func handler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Read incoming data into struct
	var inputData InputData
	err := json.NewDecoder(r.Body).Decode(&inputData)
	if err != nil {
		outputError(w, err)
		return
	}
	log.Printf("%v", inputData)

	var result Result
	result.Maps = make(map[int]Data, len(inputData.Urls))
	var page string
	var wg sync.WaitGroup
	semaphoreChannel := make(chan struct{}, MaxGoroutines) // Use the buffered channel of structs as semaphore
	errors := make(chan error, 1)
	num := 0

	for _, url := range inputData.Urls {
		semaphoreChannel <- struct{}{}
		wg.Add(1)

		go func(url string, num int, result *Result, wg *sync.WaitGroup) {
			defer wg.Done()
			page, err = fetchPageContent(url)
			if err != nil {
				errors <- err
				return
			}
			// Fill the result map with prevent race conditions
			result.mx.RLock()
			result.Maps[num] = Data{Url: url, Body: page}
			result.mx.RUnlock()

			<-semaphoreChannel
		}(url, num, &result, &wg)
		num++

		// Wait for all goroutines done
		if num == len(inputData.Urls) {
			wg.Wait()
		}

		// Handle errors and user cancellation
		select {
		case err := <-errors:
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
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	<-signalChannel
	log.Println("Shutting down the server gracefully...")

	// Shutdown gracefully
	ctx, cancel := context.WithTimeout(context.Background(), ServerShutdownTimeoutSec*time.Second)
	defer cancel()
	server.SetKeepAlivesEnabled(false)
	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("%v", err)
	}
}