# OpenRead

First MVP scaffold for an open reading tool inspired by Readlang.

## Stack

- Frontend: Vue 3 + TypeScript + Vite
- Backend: Go
- Database: SQLite

## First version

- Upload `.txt` and `.md` documents
- Store document text in SQLite
- Render text in a viewport-fitted reading view
- Keep word-based highlights across pages
- Translate highlighted text groups through Groq

## Translation setup

Set `GROQ_API_KEY` before running the backend.

```bash
export GROQ_API_KEY="your-api-key"
```

Optional:

```bash
export GROQ_MODEL="llama-3.1-8b-instant"
```

## Run the backend

```bash
cd backend
go mod tidy
go run .
```

The API runs on `http://localhost:8080`.

## Run the frontend

```bash
cd frontend
npm install
npm run dev
```

The app runs on `http://localhost:5173` and proxies `/api` to the Go backend.
