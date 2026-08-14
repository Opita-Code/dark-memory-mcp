#!/usr/bin/env python3
"""
Renderiza archivos Markdown a HTML con Mermaid, sirviendo en localhost.

Uso:
    python render_md.py <archivo.md> [puerto]

Abre http://localhost:PUERTO/ en el browser para ver el renderizado.
"""

import sys
import os
import http.server
import socketserver
import threading
import markdown
from pathlib import Path

HTML_TEMPLATE = """<!DOCTYPE html>
<html lang="es">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{title}</title>
<style>
  :root {{
    --color-fg-default: #1f2328;
    --color-fg-muted: #59636e;
    --color-canvas-default: #ffffff;
    --color-canvas-subtle: #f6f8fa;
    --color-border-default: #d1d9e0;
    --color-accent-fg: #0969da;
    --color-success-fg: #1a7f37;
    --color-attention-fg: #9a6700;
    --color-danger-fg: #d1242f;
    --font-stack: -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans", Helvetica, Arial, sans-serif, "Apple Color Emoji", "Segoe UI Emoji";
  }}
  body {{
    font-family: var(--font-stack);
    line-height: 1.6;
    color: var(--color-fg-default);
    background-color: var(--color-canvas-default);
    max-width: 980px;
    margin: 0 auto;
    padding: 45px;
  }}
  h1, h2, h3, h4, h5, h6 {{
    margin-top: 24px;
    margin-bottom: 16px;
    font-weight: 600;
    line-height: 1.25;
  }}
  h1 {{ font-size: 2em; border-bottom: 1px solid var(--color-border-default); padding-bottom: 0.3em; }}
  h2 {{ font-size: 1.5em; border-bottom: 1px solid var(--color-border-default); padding-bottom: 0.3em; }}
  h3 {{ font-size: 1.25em; }}
  h4 {{ font-size: 1em; }}
  blockquote {{
    margin: 0;
    padding: 0 1em;
    color: var(--color-fg-muted);
    border-left: 0.25em solid var(--color-border-default);
  }}
  code {{
    padding: 0.2em 0.4em;
    margin: 0;
    font-size: 85%;
    background-color: var(--color-canvas-subtle);
    border-radius: 6px;
    font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
  }}
  pre {{
    padding: 16px;
    overflow: auto;
    font-size: 85%;
    line-height: 1.45;
    background-color: var(--color-canvas-subtle);
    border-radius: 6px;
  }}
  pre code {{
    padding: 0;
    margin: 0;
    background-color: transparent;
    border: 0;
  }}
  table {{
    border-spacing: 0;
    border-collapse: collapse;
    margin: 16px 0;
    display: block;
    width: max-content;
    max-width: 100%;
    overflow: auto;
  }}
  table th, table td {{
    padding: 6px 13px;
    border: 1px solid var(--color-border-default);
  }}
  table tr {{
    background-color: var(--color-canvas-default);
    border-top: 1px solid var(--color-border-default);
  }}
  table tr:nth-child(2n) {{
    background-color: var(--color-canvas-subtle);
  }}
  a {{
    color: var(--color-accent-fg);
    text-decoration: none;
  }}
  a:hover {{
    text-decoration: underline;
  }}
  hr {{
    border: 0;
    border-top: 1px solid var(--color-border-default);
    margin: 24px 0;
  }}
  ul, ol {{
    padding-left: 2em;
  }}
  li + li {{
    margin-top: 0.25em;
  }}
  .mermaid {{
    text-align: center;
    margin: 16px 0;
    background-color: var(--color-canvas-subtle);
    border-radius: 6px;
    padding: 16px;
  }}
  .header-info {{
    color: var(--color-fg-muted);
    font-size: 0.85em;
    border-bottom: 1px solid var(--color-border-default);
    padding-bottom: 16px;
    margin-bottom: 32px;
  }}
</style>
</head>
<body>
  <div class="header-info">
    <strong>{title}</strong> · {size} palabras · renderizado para inspección visual
  </div>
  <main>
    {content}
  </main>
  <script src="https://cdn.jsdelivr.net/npm/mermaid@10/dist/mermaid.min.js"></script>
  <script>
    if (typeof mermaid !== 'undefined') {{
      mermaid.initialize({{
        startOnLoad: true,
        theme: 'default',
        securityLevel: 'loose',
        flowchart: {{ useMaxWidth: true }},
        sequence: {{ useMaxWidth: true }},
        stateDiagram: {{ useMaxWidth: true }}
      }});
    }}
  </script>
</body>
</html>
"""


def render_md_to_html(md_path: Path) -> str:
    """Renderiza un archivo .md a HTML con Mermaid."""
    md_content = md_path.read_text(encoding="utf-8")
    html_content = markdown.markdown(
        md_content,
        extensions=["fenced_code", "tables", "footnotes", "toc", "attr_list", "def_list", "sane_lists"],
    )
    size = len(md_content.split())
    return HTML_TEMPLATE.format(
        title=md_path.stem,
        size=size,
        content=html_content,
    )


def serve_html(html: str, port: int, md_name: str):
    """Sirve el HTML en localhost:port."""
    class Handler(http.server.SimpleHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.end_headers()
            self.wfile.write(html.encode("utf-8"))

        def log_message(self, format, *args):
            pass  # silencioso

    httpd = socketserver.TCPServer(("127.0.0.1", port), Handler)
    print(f"Serving {md_name} at http://127.0.0.1:{port}/", flush=True)
    httpd.serve_forever()


def main():
    if len(sys.argv) < 2:
        print("Uso: python render_md.py <archivo.md> [puerto]")
        sys.exit(1)

    md_path = Path(sys.argv[1]).resolve()
    if not md_path.exists():
        print(f"Archivo no encontrado: {md_path}")
        sys.exit(1)

    port = int(sys.argv[2]) if len(sys.argv) > 2 else 8000

    html = render_md_to_html(md_path)
    serve_html(html, port, md_path.name)


if __name__ == "__main__":
    main()
