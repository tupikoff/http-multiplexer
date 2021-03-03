package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.RequestURI)
	}))
	defer ts.Close()

	var in InputData
	var out OutputData
	for i := 1; i < MaxIncomingUrls+1; i++ {
		uri := fmt.Sprintf("/%d", i)
		url := ts.URL + uri
		in.Urls = append(in.Urls, url)
		out.Pages = append(out.Pages, Data{Url: url, Body: uri})
	}

	reqb, _ := json.Marshal(in)
	resb, _ := json.Marshal(out)

	req := string(reqb)
	res := string(resb)

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(req))
	responseRecorder := httptest.NewRecorder()

	handler(responseRecorder, request)

	if strings.TrimSpace(responseRecorder.Body.String()) != res {
		t.Errorf("want \n%s, got \n%s", res, responseRecorder.Body)
	}
}
