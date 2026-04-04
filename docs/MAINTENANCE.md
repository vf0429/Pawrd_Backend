# Pawrd Backend Maintenance Guide

This backend serves the Pawrd iOS application. It is built with Go and serves static JSON data.

## Project Structure

- **main.go**: The core server logic. It listens on port 8000 and serves the `/vaccines` endpoint.
- **vaccines.json**: The data file containing vaccine information.
- **go.mod**: The Go module definition.

## How to Run

1.  **Install Go**: Ensure Go is installed on your system.
2.  **Run Server**:
    ```bash
    go run main.go
    ```
    The server will start at `http://localhost:8000`.

## API Endpoints

### GET /vaccines
Returns a JSON list of vaccines.
- **URL**: `http://localhost:8000/vaccines`
- **Response**: JSON array of vaccine objects.

## Future Maintenance

- **Adding Data**: Edit `vaccines.json` to add or modify vaccines. The server typically needs a restart to pick up changes unless implemented to read on every request (current implementation reads on every request, so hot-reloading data works).
- **Changing Port**: Modify the `port` constant in `main.go`.
- **Deployment**: To deploy, compile the binary using `go build -o server` and run the executable. Ensure `vaccines.json` is in the same directory or properly referenced.
