package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const maxUploadSize = 5 << 20

type server struct {
	db         *sql.DB
	groqAPIKey string
	groqModel  string
	httpClient *http.Client
}

type document struct {
	ID        int64     `json:"id"`
	Filename  string    `json:"filename"`
	Content   string    `json:"content,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type translationRequest struct {
	Texts          []string `json:"texts"`
	TargetLanguage string   `json:"targetLanguage"`
}

type documentCreateRequest struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

type translationResponse struct {
	Translations []translatedText `json:"translations"`
}

type translatedText struct {
	Text        string `json:"text"`
	Translation string `json:"translation"`
}

type groqChatRequest struct {
	Model       string            `json:"model"`
	Messages    []groqChatMessage `json:"messages"`
	Temperature float64           `json:"temperature"`
}

type groqChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type groqChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func main() {
	if err := os.MkdirAll("data", 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join("data", "openread.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		log.Fatalf("init db: %v", err)
	}

	groqAPIKey := requiredEnv("GROQ_API_KEY")

	s := &server{
		db:         db,
		groqAPIKey: groqAPIKey,
		groqModel:  envOrDefault("GROQ_MODEL", "llama-3.1-8b-instant"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/documents", s.handleListDocuments)
	mux.HandleFunc("GET /api/documents/{id}", s.handleGetDocument)
	mux.HandleFunc("POST /api/documents", s.handleUploadDocument)
	mux.HandleFunc("POST /api/translations", s.handleTranslateSelections)

	addr := ":8080"
	log.Printf("backend listening on %s", addr)
	if err := http.ListenAndServe(addr, withCORS(mux)); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func initDB(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS documents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			filename TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at TEXT NOT NULL
		)
	`)
	return err
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) handleListDocuments(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT id, filename, created_at FROM documents ORDER BY id DESC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load documents")
		return
	}
	defer rows.Close()

	documents := make([]document, 0)
	for rows.Next() {
		var doc document
		var createdAt string
		if err := rows.Scan(&doc.ID, &doc.Filename, &createdAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read documents")
			return
		}
		doc.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to parse document timestamp")
			return
		}
		documents = append(documents, doc)
	}

	writeJSON(w, http.StatusOK, documents)
}

func (s *server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var doc document
	var createdAt string
	err := s.db.QueryRow(`SELECT id, filename, content, created_at FROM documents WHERE id = ?`, id).
		Scan(&doc.ID, &doc.Filename, &doc.Content, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load document")
		return
	}

	doc.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to parse document timestamp")
		return
	}

	writeJSON(w, http.StatusOK, doc)
}

func (s *server) handleUploadDocument(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxUploadSize); err != nil {
			writeError(w, http.StatusBadRequest, "invalid upload payload")
			return
		}

		file, header, err := r.FormFile("document")
		if err != nil {
			writeError(w, http.StatusBadRequest, "document file is required")
			return
		}
		defer file.Close()

		content, err := readUploadedText(file, header)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		doc, err := s.saveDocument(header.Filename, content)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusCreated, doc)
		return
	}

	var request documentCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload payload")
		return
	}

	content := strings.TrimSpace(request.Content)
	if content == "" {
		writeError(w, http.StatusBadRequest, "document text is required")
		return
	}

	filename := strings.TrimSpace(request.Filename)
	if filename == "" {
		filename = "Pasted note"
	}

	doc, err := s.saveDocument(filename, content)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, doc)
}

func (s *server) saveDocument(filename, content string) (document, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.Exec(
		`INSERT INTO documents (filename, content, created_at) VALUES (?, ?, ?)`,
		filename,
		content,
		now,
	)
	if err != nil {
		return document{}, fmt.Errorf("failed to save document")
	}

	id, err := result.LastInsertId()
	if err != nil {
		return document{}, fmt.Errorf("failed to load saved document")
	}

	createdAt, err := time.Parse(time.RFC3339, now)
	if err != nil {
		return document{}, fmt.Errorf("failed to parse save timestamp")
	}

	return document{
		ID:        id,
		Filename:  filename,
		Content:   content,
		CreatedAt: createdAt,
	}, nil
}

func (s *server) handleTranslateSelections(w http.ResponseWriter, r *http.Request) {
	var request translationRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid translation payload")
		return
	}

	texts := make([]string, 0, len(request.Texts))
	for _, text := range request.Texts {
		trimmed := strings.TrimSpace(text)
		if trimmed != "" {
			texts = append(texts, trimmed)
		}
	}
	if len(texts) == 0 {
		writeError(w, http.StatusBadRequest, "at least one text selection is required")
		return
	}

	targetLanguage := strings.TrimSpace(request.TargetLanguage)
	if targetLanguage == "" {
		targetLanguage = "English"
	}

	translations, err := s.translateTexts(r.Context(), texts, targetLanguage)
	if err != nil {
		log.Printf("translate selections: %v", err)
		writeError(w, http.StatusBadGateway, "translation request failed")
		return
	}

	writeJSON(w, http.StatusOK, translationResponse{Translations: translations})
}

func readUploadedText(file multipart.File, header *multipart.FileHeader) (string, error) {
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".txt" && ext != ".md" {
		return "", fmt.Errorf("only .txt and .md files are supported in the first version")
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("failed to read uploaded file")
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("uploaded document is empty")
	}

	return content, nil
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func (s *server) translateTexts(ctx context.Context, texts []string, targetLanguage string) ([]translatedText, error) {
	translations := make([]translatedText, len(texts))
	for i, text := range texts {
		translation, err := s.translateText(ctx, text, targetLanguage)
		if err != nil {
			return nil, fmt.Errorf("translate item %d: %w", i, err)
		}

		translations[i] = translatedText{
			Text:        text,
			Translation: translation,
		}
	}

	return translations, nil
}

func (s *server) translateText(ctx context.Context, text, targetLanguage string) (string, error) {
	body, err := json.Marshal(groqChatRequest{
		Model: s.groqModel,
		Messages: []groqChatMessage{
			{Role: "system", Content: "You are a precise translation engine. The input may be a single word, inflected form, sentence fragment, or full sentence. Always translate the exact input literally into the target language. Never ask for more context. Never explain. Never refuse. If the input is already in the target language or is ambiguous, return the closest literal translation or the original text. Return only the translated text with no commentary, no markdown, and no quotes."},
			{Role: "user", Content: buildTranslationPrompt(text, targetLanguage)},
		},
		Temperature: 0,
	})
	if err != nil {
		return "", fmt.Errorf("marshal groq request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create groq request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.groqAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("perform groq request: %w", err)
	}
	defer resp.Body.Close()

	var groqResp groqChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&groqResp); err != nil {
		return "", fmt.Errorf("decode groq response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		if groqResp.Error != nil && groqResp.Error.Message != "" {
			return "", errors.New(groqResp.Error.Message)
		}
		return "", fmt.Errorf("groq returned status %d", resp.StatusCode)
	}
	if len(groqResp.Choices) == 0 {
		return "", errors.New("groq returned no choices")
	}

	translation := strings.TrimSpace(stripMarkdownFences(groqResp.Choices[0].Message.Content))
	if translation == "" {
		return "", errors.New("groq returned an empty translation")
	}

	return translation, nil
}

func buildTranslationPrompt(text, targetLanguage string) string {
	var prompt strings.Builder
	prompt.WriteString("Translate the following text into ")
	prompt.WriteString(targetLanguage)
	prompt.WriteString(". The text may be only one word or a fragment. Preserve meaning, punctuation, and tone. Return only the translated text.\n\nTEXT:\n")
	prompt.WriteString(text)
	return prompt.String()
}

func stripMarkdownFences(value string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)
	trimmed = strings.TrimPrefix(trimmed, "text")
	return strings.TrimSpace(trimmed)
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}

	return fallback
}

func requiredEnv(key string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		log.Fatalf("missing required environment variable %s", key)
	}

	return value
}
