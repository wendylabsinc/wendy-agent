from pathlib import Path
import json

prompt = Path("prompts/system.txt").read_text().strip()
config = json.loads(Path("config/runtime.json").read_text())
sample = json.loads(Path("data/sample-input.json").read_text())

print(f"prompt={prompt}")
print(f"threshold={config['threshold']}")
print(f"sample={sample['id']}")
