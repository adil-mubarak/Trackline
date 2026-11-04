package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type chatResponse struct {
	Reply string `json:"reply"`
	Model string `json:"model"`
	Error string `json:"error"`
}

func main() {
	endpoint := os.Getenv("AI_CHAT_URL")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8087/ai/chat"
	}
	payload, err := json.Marshal(map[string]string{
		"message": "Reply briefly with a greeting and identify yourself as the connected OpenRouter model.",
	})
	if err != nil {
		fatalf("encode chat request: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		fatalf("create chat request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 100 * time.Second}).Do(request)
	if err != nil {
		fatalf("call local AI service: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var result chatResponse
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result)
		if result.Error != "" {
			fatalf("AI service returned HTTP %d: %s", response.StatusCode, result.Error)
		}
		fatalf("AI service returned HTTP %d", response.StatusCode)
	}
	var result chatResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		fatalf("decode AI service response: %v", err)
	}
	if result.Reply == "" {
		fatalf("AI service returned an empty reply")
	}
	fmt.Printf("%s\n", result.Reply)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
