
# OpenRead

First MVP scaffold for an open reading tool inspired by Readlang.

<img width="1380" height="666" alt="screenshot-2026-06-08_18-08-40" src="https://github.com/user-attachments/assets/2dc08b6b-eb18-4752-952f-8e7a51f85b47" />

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

## Run with Docker Compose

### Production-style stack

Create a local `.env` file first:

```bash
cp .env.example .env
```

Then set your values in `.env` and start both services:

```bash
docker compose up --build
```

The frontend is available at `http://localhost:5173`.
The backend API is available at `http://localhost:8080`.

The SQLite database is stored in the named Docker volume `backend-data`.

### Development stack

For live reload with Vite and Air, use the separate development file:

```bash
docker compose -f docker-compose.dev.yml up --build
```

This development setup uses:

- Frontend runs with Vite at `http://localhost:5173`
- Backend runs with Air at `http://localhost:8080`
- Source folders are bind-mounted into the containers
- Frontend dependencies live in the `frontend-node-modules` volume
- Go module and build caches live in `backend-go-mod` and `backend-go-build`
- SQLite data lives in the `backend-data` volume

Changes to frontend and backend files should reload automatically while Compose is running.
