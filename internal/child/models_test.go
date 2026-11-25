package child

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type modelObject struct {
	Object  string `json:"object"`
	ID      string `json:"id"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type modelsResponse struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

func getModels(t *testing.T, h http.Handler, key string) (int, modelsResponse) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, requestWithKey(http.MethodGet, "/v1/models", key))
	var resp modelsResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode /v1/models body: %v (%s)", err, rr.Body.String())
		}
	}
	return rr.Code, resp
}

func TestModelsEmptyCatalog(t *testing.T) {
	_, h, key := readyServer(t)
	code, resp := getModels(t, h, key)
	if code != http.StatusOK {
		t.Fatalf("code: %d, want 200", code)
	}
	if resp.Object != "list" {
		t.Fatalf("object: %q, want list", resp.Object)
	}
	if resp.Data == nil || len(resp.Data) != 0 {
		t.Fatalf("data: %+v, want empty non-null array", resp.Data)
	}
}

func TestModelsReturnsAliasCatalog(t *testing.T) {
	srv, h, key := readyServer(t)
	p1, err := srv.db.AddProvider("openai", "https://api.openai.com/v1", "")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := srv.db.AddProvider("anthropic", "https://api.anthropic.com/v1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.db.AddModel(p1.ID, "gpt-4o", true, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.db.AddModel(p2.ID, "claude-sonnet", true, true, nil); err != nil {
		t.Fatal(err)
	}
	big, err := srv.db.AddAlias("big", []string{"openai/gpt-4o", "anthropic/claude-sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.db.AddAlias("small", []string{"openai/gpt-4o"}); err != nil {
		t.Fatal(err)
	}

	code, resp := getModels(t, h, key)
	if code != http.StatusOK {
		t.Fatalf("code: %d, want 200", code)
	}
	if resp.Object != "list" || len(resp.Data) != 2 {
		t.Fatalf("resp: %+v, want list with 2 models", resp)
	}
	// Aliases come back ordered by name: big, small.
	if resp.Data[0].ID != "big" || resp.Data[1].ID != "small" {
		t.Fatalf("model ids: %q, %q, want big, small", resp.Data[0].ID, resp.Data[1].ID)
	}
	for _, m := range resp.Data {
		if m.Object != "model" || m.OwnedBy != "acme" || m.Created <= 0 {
			t.Fatalf("unexpected model object: %+v", m)
		}
	}
	if resp.Data[0].Created != big.CreatedAt.Unix() {
		t.Fatalf("created: %d, want %d", resp.Data[0].Created, big.CreatedAt.Unix())
	}

	// Deleting an alias removes it from the catalog.
	if err := srv.db.DeleteAlias("big"); err != nil {
		t.Fatal(err)
	}
	_, resp = getModels(t, h, key)
	if len(resp.Data) != 1 || resp.Data[0].ID != "small" {
		t.Fatalf("after delete: %+v, want only small", resp.Data)
	}
}

func TestModelsRequiresKey(t *testing.T) {
	_, h, _ := readyServer(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no key: %d, want 401", rr.Code)
	}
}
