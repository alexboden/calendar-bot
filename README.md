docker build -t calendar-bot .
docker run --rm -v "$(pwd)/processed_ids.txt:/app/processed_ids.txt" calendar-bot