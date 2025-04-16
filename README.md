docker build -t calendar-bot .
docker run -d --log-opt max-size=10m --log-opt max-file=3 -v "$(pwd)/processed_ids.txt:/app/processed_ids.txt" calendar-bot