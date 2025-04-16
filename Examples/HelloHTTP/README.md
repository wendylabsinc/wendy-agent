curl http://host.docker.internal:11434/api/generate -d '{
  "model": "tinyllama:latest",
  "prompt": "Why is the sky blue?"
}'

I am using `tinyllama:latest `