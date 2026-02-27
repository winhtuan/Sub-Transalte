# Sub-Translate — Run Guide

## GPU Mode (NVIDIA CUDA)

```bash
# Start (first time builds images)
docker compose --profile gpu up -d --build

# Start (subsequent runs)
docker compose --profile gpu up -d

# View logs
docker compose logs -f
```

## CPU Mode

```bash
docker compose --profile cpu up -d
```

## Stop & Cleanup

```bash
# Stop all containers
docker compose down

# Stop + remove volumes (model cache)
docker compose down -v

# Remove specific container
docker rm -f libretranslate
```
