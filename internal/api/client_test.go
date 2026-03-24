package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient_PrefixesHTTP(t *testing.T) {
	c := NewClient("vps:7749", "")
	if c.baseURL != "http://vps:7749" {
		t.Errorf("expected http://vps:7749, got %s", c.baseURL)
	}

	c2 := NewClient("https://secure.example.com", "")
	if c2.baseURL != "https://secure.example.com" {
		t.Errorf("expected https://secure.example.com, got %s", c2.baseURL)
	}
}

func TestClient_GetStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Error("missing API key header")
		}
		resp := StatusResponse{
			DeployID:  "test-deploy",
			Alive:     true,
			UptimeSec: 120,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key")
	status, err := client.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status.DeployID != "test-deploy" {
		t.Errorf("expected test-deploy, got %s", status.DeployID)
	}
	if !status.Alive {
		t.Error("expected alive=true")
	}
}

func TestClient_GetTasks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tasks := []TaskResponse{
			{ID: 1, IssueNumber: 42, IssueTitle: "Test issue", Status: "running"},
			{ID: 2, IssueNumber: 55, IssueTitle: "Another issue", Status: "done"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tasks)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "")
	tasks, err := client.GetTasks()
	if err != nil {
		t.Fatalf("GetTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].IssueNumber != 42 {
		t.Errorf("expected issue 42, got %d", tasks[0].IssueNumber)
	}
}

func TestClient_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(errorResponse{"unauthorized", "invalid key"})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "wrong-key")
	_, err := client.GetStatus()
	if err == nil {
		t.Fatal("expected auth error")
	}
	if err.Error() != "authentication failed — check --api-key or MINDER_API_KEY" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_Stop(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/stop" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		called = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"stop signal sent"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "")
	err := client.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !called {
		t.Error("stop handler was not called")
	}
}

func TestClient_ConnectionRefused(t *testing.T) {
	client := NewClient("localhost:1", "")
	_, err := client.GetStatus()
	if err == nil {
		t.Fatal("expected connection error")
	}
	// Should get a friendly error message.
	if err.Error() != "connection refused — is the daemon running with --serve?" {
		t.Logf("got error: %v", err)
	}
}

func TestClient_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(errorResponse{"db_error", "database locked"})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "")
	_, err := client.GetStatus()
	if err == nil {
		t.Fatal("expected error")
	}
	expected := "remote error (500): database locked"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}
