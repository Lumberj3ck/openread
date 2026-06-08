# OpenRead

OpenRead is a personal reading app for plain text and markdown documents.

You can upload a file or paste text directly, open it in a focused reading view, highlight words or phrases, and translate selected passages inline.

<img width="1380" height="666" alt="screenshot-2026-06-08_18-08-40" src="https://github.com/user-attachments/assets/2dc08b6b-eb18-4752-952f-8e7a51f85b47" />

## Features

- Upload `.txt` and `.md` files
- Paste text directly into the library screen
- Store documents locally in SQLite
- Open each document at its own reloadable URL
- Highlight text selections in the reader
- Translate selected passages through Groq
- Adjust reader font, size, width, and highlight color
- Persist reader preferences across reloads

## Requirements

- `GROQ_API_KEY` is required
- `GROQ_MODEL` is optional

Create a local `.env` file:

```bash
cp .env.example .env
```

Then set at least:

```env
GROQ_API_KEY=your-api-key
GROQ_MODEL=llama-3.1-8b-instant
```

## Local Development

### Backend

```bash
cd backend
go mod tidy
go run .
```

The backend runs on `http://localhost:8080`.

### Frontend

```bash
cd frontend
npm install
npm run dev
```

The frontend runs on `http://localhost:5173`.

## Docker

### Production-style Compose

```bash
docker compose up --build
```

- Frontend: `http://localhost:5173`
- Backend: `http://localhost:8080`

### Development Compose

```bash
docker compose -f docker-compose.dev.yml up --build
```

This development setup uses:

- Vite for frontend hot reload
- Air for Go rebuilds
- bind mounts for `frontend/` and `backend/`
- named volumes for SQLite data and dependency caches

## Data

- Local backend runs SQLite in `backend/data/openread.db`
- Docker stores SQLite data in the `backend-data` volume

## Notes

- The backend fails on startup if `GROQ_API_KEY` is missing
- Reader routes use hash URLs such as `#/documents/12` so reloading a document stays on that document
