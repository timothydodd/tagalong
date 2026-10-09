# Base images come from AWS's public mirror of the Docker official images
# (public.ecr.aws/docker/library) rather than Docker Hub, whose anonymous pull
# rate limit fails CI builds on shared GitHub runners. No "# syntax=" line: it
# would fetch the frontend from Docker Hub, and BuildKit's built-in one suffices.

# --- Stage 1: build the React UI ---
FROM public.ecr.aws/docker/library/node:22-alpine AS ui
WORKDIR /ui
COPY ui/package.json ui/package-lock.json* ./
RUN npm install
COPY ui/ ./
RUN npm run build

# --- Stage 2: build the Go binary (with the built UI embedded) ---
FROM public.ecr.aws/docker/library/golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Overwrite the placeholder ui/dist with the freshly built assets.
COPY --from=ui /ui/dist ./ui/dist
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /tagalong ./cmd/tagalong

# --- Stage 3: minimal runtime ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /tagalong /tagalong
EXPOSE 8080
VOLUME /data
ENTRYPOINT ["/tagalong"]
