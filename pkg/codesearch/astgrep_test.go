package codesearch_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/codesearch"
)

// requireAstGrep skips the test if ast-grep is not installed.
func requireAstGrep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ast-grep"); err != nil {
		t.Skip("ast-grep not installed, skipping")
	}
}

// pythonFixture is a realistic Python source file with classes, dataclasses,
// functions (sync and async), private members, and nesting. ~5.5KB.
const pythonFixture = `from __future__ import annotations
from dataclasses import dataclass, field
from typing import Any, Optional, Callable
import asyncio
import logging

logger = logging.getLogger(__name__)

# Constants
DEFAULT_HOST = "0.0.0.0"
DEFAULT_PORT = 8080
MAX_CONNECTIONS = 1000

@dataclass
class Config:
    host: str = DEFAULT_HOST
    port: int = DEFAULT_PORT
    debug: bool = False
    max_connections: int = MAX_CONNECTIONS
    read_timeout: float = 30.0
    write_timeout: float = 30.0
    middlewares: list[Callable] = field(default_factory=list)
    _internal_state: dict = field(default_factory=dict, repr=False)

@dataclass
class Route:
    method: str
    path: str
    handler: Callable
    middleware: list[Callable] = field(default_factory=list)
    _compiled_pattern: Optional[str] = None

class Router:
    """Route manager that supports pattern matching and middleware chains."""

    def __init__(self) -> None:
        self._routes: list[Route] = []
        self._middleware: list[Callable] = []

    def add(self, method: str, path: str, handler: Callable) -> None:
        """Register a new route with the router."""
        route = Route(method=method, path=path, handler=handler)
        route._compiled_pattern = _compile_pattern(path)
        self._routes.append(route)
        logger.debug("Added route: %s %s", method, path)

    def match(self, method: str, path: str) -> Optional[Callable]:
        """Find a matching handler for the given method and path."""
        for route in self._routes:
            if route.method == method and _match_pattern(route._compiled_pattern, path):
                return route.handler
        return None

    def routes(self) -> list[Route]:
        """Return all registered routes."""
        return list(self._routes)


# --- Server ---

class Server:
    """HTTP server with middleware support and graceful shutdown."""

    def __init__(self, config: Config) -> None:
        self._config = config
        self._router = Router()
        self._middleware: list[Callable] = list(config.middlewares)
        self._running = False
        self._connections: set = set()

    def start(self) -> None:
        """Start the server and begin accepting connections."""
        if self._running:
            raise RuntimeError("Server already running")
        self._running = True
        logger.info("Server starting on %s:%d", self._config.host, self._config.port)
        _bind_socket(self._config.host, self._config.port)

    def stop(self) -> None:
        """Gracefully stop the server, closing all connections."""
        if not self._running:
            return
        self._running = False
        for conn in self._connections:
            conn.close()
        self._connections.clear()
        logger.info("Server stopped")

    def use(self, middleware: Callable) -> None:
        """Add middleware to the server pipeline."""
        self._middleware.append(middleware)

    def handle(self, method: str, path: str, handler: Callable) -> None:
        """Register a route handler."""
        self._router.add(method, path, handler)

    async def process_request(self, method: str, path: str, body: Any) -> dict:
        """Process an incoming request through the middleware chain."""
        handler = self._router.match(method, path)
        if handler is None:
            return {"status": 404, "error": "Not Found"}

        # Apply middleware chain
        chain = handler
        for mw in reversed(self._middleware):
            chain = mw(chain)

        try:
            result = await chain(body)
            return {"status": 200, "data": result}
        except Exception as e:
            logger.exception("Request failed: %s %s", method, path)
            return {"status": 500, "error": str(e)}


# --- Factory functions ---

def create_server(config: Optional[Config] = None) -> Server:
    """Create a new server instance with optional configuration."""
    if config is None:
        config = Config()
    return Server(config)


# --- Middleware ---

async def health_check() -> dict:
    """Simple health check endpoint."""
    return {"status": "ok", "version": "1.0.0"}


def logging_middleware(handler: Callable) -> Callable:
    """Middleware that logs request details."""
    async def wrapper(body: Any) -> Any:
        logger.info("Processing request")
        result = await handler(body)
        logger.info("Request completed")
        return result
    return wrapper


def rate_limit_middleware(limit: int = 100) -> Callable:
    """Middleware that enforces rate limiting."""
    _counter: dict[str, int] = {}

    def middleware(handler: Callable) -> Callable:
        async def wrapper(body: Any) -> Any:
            return await handler(body)
        return wrapper
    return middleware


# --- Validators ---

def validate_config(config: Config) -> list[str]:
    """Validate server configuration, returning a list of errors."""
    errors: list[str] = []
    if config.port < 1 or config.port > 65535:
        errors.append(f"Invalid port: {config.port}")
    if config.max_connections < 1:
        errors.append("max_connections must be positive")
    if config.read_timeout <= 0:
        errors.append("read_timeout must be positive")
    if config.write_timeout <= 0:
        errors.append("write_timeout must be positive")
    return errors


# --- Private helpers ---

def _parse_path(path: str) -> list[str]:
    """Split a URL path into segments."""
    return [s for s in path.split("/") if s]


def _format_duration(seconds: float) -> str:
    """Format a duration in human-readable form."""
    if seconds < 1:
        return f"{seconds * 1000:.0f}ms"
    if seconds < 60:
        return f"{seconds:.1f}s"
    minutes = int(seconds // 60)
    remaining = seconds % 60
    return f"{minutes}m{remaining:.0f}s"


class _RequestContext:

    def __init__(self, method: str, path: str):
        self.method = method
        self.path = path
        self.start_time = 0.0
        self.headers: dict[str, str] = {}


def _compile_pattern(pattern: str) -> str:
    return pattern.replace("{", "(?P<").replace("}", ">[^/]+)")

def _match_pattern(pattern: Optional[str], path: str) -> bool:
    if pattern is None:
        return False
    import re
    return re.match(pattern, path) is not None

def _bind_socket(host: str, port: int) -> None:
    pass
`

// typescriptFixture is a realistic TypeScript source file with interfaces,
// type aliases, enums, classes, functions, and exports. ~4.5KB.
const typescriptFixture = `import { EventEmitter } from 'events';
import type { IncomingMessage, ServerResponse } from 'http';

export const DEFAULT_PORT = 8080;
export const MAX_CONNECTIONS = 1000;

interface Handler {
  handle(req: IncomingMessage): Promise<ServerResponse>;
  name: string;
  priority?: number;
}

interface Middleware {
  process(req: IncomingMessage, next: () => Promise<void>): Promise<void>;
}

type Config = {
  port: number;
  host: string;
  debug: boolean;
  maxConnections: number;
  readTimeout: number;
  writeTimeout: number;
}

type Result<T> = {
  success: boolean;
  data?: T;
  error?: string;
  timestamp: number;
}

enum HttpMethod {
  GET = 'GET',
  POST = 'POST',
  PUT = 'PUT',
  DELETE = 'DELETE',
  PATCH = 'PATCH',
  OPTIONS = 'OPTIONS',
}

enum StatusCode {
  OK = 200,
  Created = 201,
  BadRequest = 400,
  NotFound = 404,
  InternalServerError = 500,
}

class Route {
  constructor(
    public method: HttpMethod,
    public path: string,
    public handler: Handler,
    public middleware: Middleware[] = [],
  ) {}
}

class Router {
  private routes: Route[] = [];
  private middleware: Middleware[] = [];

  addRoute(method: HttpMethod, path: string, handler: Handler): void {
    this.routes.push(new Route(method, path, handler));
  }

  use(middleware: Middleware): void {
    this.middleware.push(middleware);
  }

  match(method: HttpMethod, path: string): Handler | undefined {
    const route = this.routes.find(
      (r) => r.method === method && r.path === path
    );
    return route?.handler;
  }

  getRoutes(): Route[] {
    return [...this.routes];
  }

  removeRoute(method: HttpMethod, path: string): boolean {
    const index = this.routes.findIndex(
      (r) => r.method === method && r.path === path
    );
    if (index === -1) return false;
    this.routes.splice(index, 1);
    return true;
  }
}

class Server implements Handler {
  name = 'MainServer';
  private router: Router;
  private config: Config;
  private running = false;
  private connections: Set<string> = new Set();
  private emitter: EventEmitter;

  constructor(config: Partial<Config> = {}) {
    this.config = {
      port: config.port ?? DEFAULT_PORT,
      host: config.host ?? '0.0.0.0',
      debug: config.debug ?? false,
      maxConnections: config.maxConnections ?? MAX_CONNECTIONS,
      readTimeout: config.readTimeout ?? 30000,
      writeTimeout: config.writeTimeout ?? 30000,
    };
    this.router = new Router();
    this.emitter = new EventEmitter();
  }

  async handle(req: IncomingMessage): Promise<ServerResponse> {
    return {} as ServerResponse;
  }

  start(): void {
    if (this.running) throw new Error('Server already running');
    this.running = true;
    this.emitter.emit('start');
  }

  stop(): void {
    if (!this.running) return;
    this.running = false;
    this.connections.clear();
    this.emitter.emit('stop');
  }

  use(middleware: Middleware): void {
    this.router.use(middleware);
  }
}

function createServer(config: Partial<Config> = {}): Server {
  return new Server(config);
}

interface _InternalHandler {
  _process(data: unknown): void;
}

type _CacheEntry = {
  key: string;
  value: unknown;
  ttl: number;
}

function healthCheck(): Promise<Result<string>> {
  return Promise.resolve({ success: true, data: 'ok', timestamp: Date.now() });
}

function loggingMiddleware(): Middleware {
  return {
    async process(req: IncomingMessage, next: () => Promise<void>) {
      const start = Date.now();
      console.log('Request started');
      await next();
      const duration = Date.now() - start;
      console.log('Request completed in ' + duration + 'ms');
    },
  };
}

function validateConfig(config: Config): string[] {
  const errors: string[] = [];
  if (config.port < 1 || config.port > 65535) {
    errors.push('Invalid port');
  }
  if (config.maxConnections < 1) {
    errors.push('maxConnections must be positive');
  }
  if (config.readTimeout <= 0) {
    errors.push('readTimeout must be positive');
  }
  if (config.writeTimeout <= 0) {
    errors.push('writeTimeout must be positive');
  }
  return errors;
}

function parsePath(path: string): string[] {
  return path.split('/').filter(Boolean);
}

function formatDuration(ms: number): string {
  if (ms < 1000) return ms + 'ms';
  if (ms < 60000) return (ms / 1000).toFixed(1) + 's';
  return Math.floor(ms / 60000) + 'm' + Math.floor((ms % 60000) / 1000) + 's';
}

type _InternalState = {
  connections: Map<string, unknown>;
  startTime: number;
}
`

// estimateTokens provides a rough token count (~4 chars per token).
func estimateTokens(s string) int {
	return len(s) / 4
}

func TestSummarizePython(t *testing.T) {
	requireAstGrep(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "server.py")
	if err := os.WriteFile(path, []byte(pythonFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	summary, err := codesearch.SummarizeFile(path)
	if err != nil {
		t.Fatalf("SummarizeFile(%s) error: %v", path, err)
	}

	origTokens := estimateTokens(pythonFixture)
	summaryTokens := estimateTokens(summary)
	savings := 1.0 - float64(summaryTokens)/float64(origTokens)
	t.Logf("original ~%d tokens, summary ~%d tokens, savings %.1f%%", origTokens, summaryTokens, savings*100)

	if savings < 0.70 {
		t.Errorf("token savings %.1f%% < 70%% threshold", savings*100)
	}

	// Check for key structural elements in the summary.
	checks := []struct {
		name    string
		pattern string
	}{
		{"Config class", "class Config"},
		{"Router class", "class Router"},
		{"Server class", "class Server"},
		{"create_server func", "func create_server"},
		{"process_request method", "func async process_request"},
		{"validate_config func", "func validate_config"},
		{"logging_middleware func", "func logging_middleware"},
		{"private _parse_path", "func _parse_path"},
		{"private _RequestContext", "class _RequestContext"},
	}

	for _, c := range checks {
		if !strings.Contains(summary, c.pattern) {
			t.Errorf("summary missing %s (expected %q)", c.name, c.pattern)
		}
	}

	if !strings.Contains(summary, "L") {
		t.Error("summary has no line numbers")
	}

	t.Logf("summary:\n%s", indent(summary))
}

func TestSummarizeTypeScript(t *testing.T) {
	requireAstGrep(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "server.ts")
	if err := os.WriteFile(path, []byte(typescriptFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	summary, err := codesearch.SummarizeFile(path)
	if err != nil {
		t.Fatalf("SummarizeFile(%s) error: %v", path, err)
	}

	origTokens := estimateTokens(typescriptFixture)
	summaryTokens := estimateTokens(summary)
	savings := 1.0 - float64(summaryTokens)/float64(origTokens)
	t.Logf("original ~%d tokens, summary ~%d tokens, savings %.1f%%", origTokens, summaryTokens, savings*100)

	if savings < 0.70 {
		t.Errorf("token savings %.1f%% < 70%% threshold", savings*100)
	}

	checks := []struct {
		name    string
		pattern string
	}{
		{"Handler interface", "interface Handler"},
		{"Config type", "type Config"},
		{"Result type", "type Result"},
		{"HttpMethod enum", "enum HttpMethod"},
		{"StatusCode enum", "enum StatusCode"},
		{"Route class", "class Route"},
		{"Router class", "class Router"},
		{"Server class", "class Server"},
		{"createServer func", "func createServer"},
		{"healthCheck func", "func healthCheck"},
		{"validateConfig func", "func validateConfig"},
		{"export const", "export const"},
	}

	for _, c := range checks {
		if !strings.Contains(summary, c.pattern) {
			t.Errorf("summary missing %s (expected %q)", c.name, c.pattern)
		}
	}

	t.Logf("summary:\n%s", indent(summary))
}

func TestSummarizeFileGoFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	src := "package main\n\nimport \"fmt\"\n\nfunc Hello() { fmt.Println(\"hi\") }\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	summary, err := codesearch.SummarizeFile(path)
	if err != nil {
		t.Fatalf("SummarizeFile(%s) error: %v", path, err)
	}

	if !strings.Contains(summary, "Hello") {
		t.Errorf("Go summary missing Hello function, got: %s", summary)
	}
}

func TestSummarizeFileUnsupportedExtension(t *testing.T) {
	_, err := codesearch.SummarizeFile("/tmp/data.csv")
	if err == nil {
		t.Fatal("expected error for unsupported extension, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("error should mention 'unsupported', got: %v", err)
	}
}

func TestSummarizeFileAstGrepNotInstalled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.py")
	if err := os.WriteFile(path, []byte("def hello(): pass\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", "")
	defer os.Setenv("PATH", origPath)

	_, err := codesearch.SummarizeFile(path)
	if err == nil {
		t.Fatal("expected error when ast-grep not in PATH")
	}
	if !strings.Contains(err.Error(), "ast-grep") {
		t.Errorf("error should mention ast-grep, got: %v", err)
	}
}

func TestSummarizeFilePythonTestFile(t *testing.T) {
	requireAstGrep(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "test_handler.py")
	src := "import pytest\n\ndef test_create():\n    pass\n\ndef test_delete():\n    pass\n\nclass TestHandler:\n    def test_get(self):\n        pass\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	summary, err := codesearch.SummarizeFile(path)
	if err != nil {
		t.Fatalf("SummarizeFile(%s) error: %v", path, err)
	}

	if !strings.Contains(summary, "test_create") {
		t.Errorf("summary should contain test_create, got: %s", summary)
	}
}

// indent adds 4-space indent to each line for readable test log output.
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = "    " + line
		}
	}
	return strings.Join(lines, "\n")
}
