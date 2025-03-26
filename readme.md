# Documentation Crawler CLI

An IA generated command-line tool to crawl, parse, and save documentation websites for offline reference, analysis, or creating knowledge bases.

## Features

- Crawl any documentation website and save structured content as JSON
- Configurable CSS selectors for finding navigation elements and content areas
- Domain restriction to prevent crawling off-site
- Concurrent crawling with configurable worker count
- Extraction of page metadata, headings, text content, and code blocks
- Output as structured JSON for further processing

## Installation

### Prerequisites

- Go 1.16 or higher

### Installing from Source

```bash
# Clone the repository
git clone https://github.com/matthieukhl/go-crawler.git
cd go-crawler

# Build the binary
go build -o doccrawler

# Optional: Install globally
go install
```

## Usage

### Basic Usage

```bash
# Crawl a documentation site
doccrawler crawl https://docs.example.com

# Specify output file
doccrawler crawl https://docs.example.com -o example_docs.json

# Limit the number of pages
doccrawler crawl https://docs.example.com -p 100
```

### Full Command Reference

```bash
doccrawler crawl [url] [flags]
```

#### Flags

- `-o, --output string`: Output file (default: "{domain}\_docs.json")
- `-p, --max-pages int`: Maximum number of pages to crawl (default: 500)
- `-w, --workers int`: Number of concurrent workers (default: 8)
- `-t, --timeout duration`: Crawler timeout duration (default: 20m)
- `-d, --domain string`: Restrict crawling to this domain (default: derived from the base URL)
- `-s, --sidebar string`: CSS selector for sidebar navigation links (default: ".sidebar a, nav a, .menu a")
- `-c, --content string`: CSS selector for main content area (default: "main, article, .content, .documentation, .markdown")
- `-q, --silent`: Run in silent mode with minimal output

## Examples

### Crawl a Python Documentation Site

```bash
doccrawler crawl https://docs.python.org/3/ --max-pages 200 --output python_docs.json
```

### Crawl a JavaScript Framework with Custom Selectors

```bash
doccrawler crawl https://vuejs.org/guide/ \
  --sidebar ".sidebar-links a" \
  --content ".content" \
  --domain "vuejs.org" \
  --workers 12
```

### Create a Knowledge Base from Multiple Documentation Sites

```bash
# Create a script to crawl multiple sites
#!/bin/bash
doccrawler crawl https://docs.djangoproject.com/en/4.2/ -o django_docs.json
doccrawler crawl https://flask.palletsprojects.com/en/2.3.x/ -o flask_docs.json
doccrawler crawl https://fastapi.tiangolo.com/ -o fastapi_docs.json
```

## Output Format

The tool produces a JSON file with the following structure:

```json
[
  {
    "url": "https://docs.example.com/getting-started",
    "content": {
      "title": "Getting Started with Example",
      "headings": [
        {
          "level": 1,
          "text": "Installation",
          "content": "To install Example, run the following command..."
        },
        {
          "level": 2,
          "text": "Prerequisites",
          "content": "You'll need Python 3.8 or higher..."
        }
      ],
      "code": [
        {
          "language": "bash",
          "code": "pip install example"
        },
        {
          "language": "python",
          "code": "import example\n\nexample.initialize()"
        }
      ],
      "text": "Example is a powerful library for... It provides several features including..."
    }
  },
  {
    "url": "https://docs.example.com/api-reference",
    "content": {
      "title": "API Reference",
      "headings": [...],
      "code": [...],
      "text": "..."
    }
  }
]
```

## Advanced Usage

### Integrating with Other Tools

The JSON output is designed to be easily processed by other tools. Here are some examples:

```bash
# Use jq to extract all code examples from Python documentation
cat python_docs.json | jq '.[].content.code[] | select(.language=="python") | .code'

# Convert documentation to Markdown files
cat framework_docs.json | jq -r '.[] | "# " + .content.title + "\n\n" + .content.text' > framework_overview.md
```

### Creating a Custom Directory Structure

You can process the JSON output to create organized documentation:

```python
import json
import os
import re

with open('docs.json', 'r') as f:
    docs = json.load(f)

for page in docs:
    # Create a clean filename from the URL or title
    filename = re.sub(r'[^a-zA-Z0-9_-]', '_', page['content']['title']) + '.md'
    with open('docs/' + filename, 'w') as f:
        f.write(f"# {page['content']['title']}\n\n")
        f.write(f"Source: {page['url']}\n\n")
        f.write(page['content']['text'])
```

## Troubleshooting

### Common Issues

1. **Crawler not finding links**: Check if the `--sidebar` selector matches the navigation elements on the site. Try using browser developer tools to find the correct selector.

2. **Getting too many irrelevant pages**: Use the `--domain` flag to restrict crawling to specific subdomains.

3. **Content not being extracted properly**: Adjust the `--content` selector to match the main content area of the documentation.

## License

MIT
