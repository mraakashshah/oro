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

// rustFixture is a realistic Rust source file with structs, impls, traits,
// enums, functions, and pub visibility modifiers. ~4KB.
const rustFixture = `use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::fmt;

pub const DEFAULT_PORT: u16 = 8080;
pub const MAX_CONNECTIONS: usize = 1000;

pub type Result<T> = std::result::Result<T, Box<dyn std::error::Error>>;

#[derive(Debug, Clone)]
pub enum HttpMethod {
    Get,
    Post,
    Put,
    Delete,
    Patch,
    Options,
}

#[derive(Debug, Clone)]
pub struct Config {
    pub host: String,
    pub port: u16,
    pub debug: bool,
    pub max_connections: usize,
}

#[derive(Debug)]
pub struct Route {
    pub method: HttpMethod,
    pub path: String,
    handler: Box<dyn Fn(&Request) -> Response + Send + Sync>,
}

#[derive(Debug, Clone)]
pub struct Request {
    pub method: HttpMethod,
    pub path: String,
    pub headers: HashMap<String, String>,
    pub body: Vec<u8>,
}

#[derive(Debug, Clone)]
pub struct Response {
    pub status: u16,
    pub headers: HashMap<String, String>,
    pub body: Vec<u8>,
}

pub trait Handler: Send + Sync {
    fn handle(&self, req: &Request) -> Response;
    fn name(&self) -> &str;
}

pub trait Middleware: Send + Sync {
    fn process(&self, req: &Request, next: &dyn Handler) -> Response;
}

pub struct Router {
    routes: Vec<Route>,
    not_found: Option<Box<dyn Handler>>,
}

pub struct Server {
    config: Config,
    router: Arc<Mutex<Router>>,
    middleware: Vec<Box<dyn Middleware>>,
}

impl Config {
    pub fn new(host: impl Into<String>, port: u16) -> Self {
        Config {
            host: host.into(),
            port,
            debug: false,
            max_connections: MAX_CONNECTIONS,
        }
    }

    pub fn with_debug(mut self, debug: bool) -> Self {
        self.debug = debug;
        self
    }

    pub fn validate(&self) -> Result<()> {
        if self.port == 0 {
            return Err("port cannot be 0".into());
        }
        if self.host.is_empty() {
            return Err("host cannot be empty".into());
        }
        Ok(())
    }
}

impl Router {
    pub fn new() -> Self {
        Router {
            routes: Vec::new(),
            not_found: None,
        }
    }

    pub fn add(&mut self, method: HttpMethod, path: impl Into<String>, handler: impl Fn(&Request) -> Response + Send + Sync + 'static) {
        self.routes.push(Route {
            method,
            path: path.into(),
            handler: Box::new(handler),
        });
    }

    pub fn match_route(&self, method: &HttpMethod, path: &str) -> Option<&Route> {
        self.routes.iter().find(|r| {
            std::mem::discriminant(&r.method) == std::mem::discriminant(method)
                && r.path == path
        })
    }

    pub fn route_count(&self) -> usize {
        self.routes.len()
    }
}

impl Default for Router {
    fn default() -> Self {
        Self::new()
    }
}

impl Server {
    pub fn new(config: Config) -> Self {
        Server {
            config,
            router: Arc::new(Mutex::new(Router::new())),
            middleware: Vec::new(),
        }
    }

    pub fn use_middleware(&mut self, mw: impl Middleware + 'static) {
        self.middleware.push(Box::new(mw));
    }

    pub fn handle(&self, method: HttpMethod, path: impl Into<String>, handler: impl Fn(&Request) -> Response + Send + Sync + 'static) {
        let mut router = self.router.lock().unwrap();
        router.add(method, path, handler);
    }

    pub fn start(&self) -> Result<()> {
        self.config.validate()?;
        println!("Server starting on {}:{}", self.config.host, self.config.port);
        Ok(())
    }
}

impl fmt::Display for HttpMethod {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            HttpMethod::Get => write!(f, "GET"),
            HttpMethod::Post => write!(f, "POST"),
            HttpMethod::Put => write!(f, "PUT"),
            HttpMethod::Delete => write!(f, "DELETE"),
            HttpMethod::Patch => write!(f, "PATCH"),
            HttpMethod::Options => write!(f, "OPTIONS"),
        }
    }
}

pub fn create_server(host: impl Into<String>, port: u16) -> Server {
    let config = Config::new(host, port);
    Server::new(config)
}

pub fn health_check() -> Response {
    let mut headers = HashMap::new();
    headers.insert("Content-Type".to_string(), "application/json".to_string());
    Response {
        status: 200,
        headers,
        body: b"{\"status\": \"ok\"}".to_vec(),
    }
}

fn parse_path(path: &str) -> Vec<&str> {
    path.split('/').filter(|s| !s.is_empty()).collect()
}

fn format_duration(ms: u64) -> String {
    if ms < 1000 {
        format!("{}ms", ms)
    } else if ms < 60_000 {
        format!("{:.1}s", ms as f64 / 1000.0)
    } else {
        format!("{}m{}s", ms / 60_000, (ms % 60_000) / 1000)
    }
}
`

// javaFixture is a realistic Java source file with classes, interfaces,
// enums, and methods. ~4KB.
const javaFixture = `package com.example.server;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

public class ServerConfig {
    private final String host;
    private final int port;
    private final boolean debug;
    private final int maxConnections;

    public ServerConfig(String host, int port) {
        this.host = host;
        this.port = port;
        this.debug = false;
        this.maxConnections = 1000;
    }

    public String getHost() { return host; }
    public int getPort() { return port; }
    public boolean isDebug() { return debug; }
    public int getMaxConnections() { return maxConnections; }

    public void validate() {
        if (port < 1 || port > 65535) {
            throw new IllegalArgumentException("Invalid port: " + port);
        }
        if (host == null || host.isEmpty()) {
            throw new IllegalArgumentException("Host cannot be empty");
        }
    }
}

public interface Handler {
    Response handle(Request request);
    String getName();
}

public interface Middleware {
    Response process(Request request, Handler next);
}

public enum HttpMethod {
    GET, POST, PUT, DELETE, PATCH, OPTIONS;

    public static HttpMethod fromString(String method) {
        return HttpMethod.valueOf(method.toUpperCase());
    }
}

public class Request {
    private final HttpMethod method;
    private final String path;
    private final Map<String, String> headers;
    private final byte[] body;

    public Request(HttpMethod method, String path) {
        this.method = method;
        this.path = path;
        this.headers = new HashMap<>();
        this.body = new byte[0];
    }

    public HttpMethod getMethod() { return method; }
    public String getPath() { return path; }
    public Map<String, String> getHeaders() { return new HashMap<>(headers); }
    public byte[] getBody() { return body.clone(); }
}

public class Response {
    private final int status;
    private final Map<String, String> headers;
    private final byte[] body;

    public Response(int status) {
        this.status = status;
        this.headers = new HashMap<>();
        this.body = new byte[0];
    }

    public Response(int status, byte[] body) {
        this.status = status;
        this.headers = new HashMap<>();
        this.body = body;
    }

    public int getStatus() { return status; }
    public byte[] getBody() { return body.clone(); }
}

public class Route {
    private final HttpMethod method;
    private final String path;
    private final Handler handler;

    public Route(HttpMethod method, String path, Handler handler) {
        this.method = method;
        this.path = path;
        this.handler = handler;
    }

    public HttpMethod getMethod() { return method; }
    public String getPath() { return path; }
    public Handler getHandler() { return handler; }
}

public class Router {
    private final List<Route> routes = new ArrayList<>();

    public void addRoute(HttpMethod method, String path, Handler handler) {
        routes.add(new Route(method, path, handler));
    }

    public Optional<Handler> match(HttpMethod method, String path) {
        return routes.stream()
            .filter(r -> r.getMethod() == method && r.getPath().equals(path))
            .map(Route::getHandler)
            .findFirst();
    }

    public List<Route> getRoutes() {
        return new ArrayList<>(routes);
    }

    public int getRouteCount() {
        return routes.size();
    }
}

public class Server {
    private final ServerConfig config;
    private final Router router;
    private final List<Middleware> middlewares;
    private boolean running;

    public Server(ServerConfig config) {
        this.config = config;
        this.router = new Router();
        this.middlewares = new ArrayList<>();
        this.running = false;
    }

    public void addMiddleware(Middleware middleware) {
        middlewares.add(middleware);
    }

    public void handle(HttpMethod method, String path, Handler handler) {
        router.addRoute(method, path, handler);
    }

    public void start() {
        config.validate();
        this.running = true;
        System.out.printf("Server starting on %s:%d%n", config.getHost(), config.getPort());
    }

    public void stop() {
        this.running = false;
    }

    public boolean isRunning() { return running; }
}

public class ServerFactory {
    public static Server create(String host, int port) {
        ServerConfig config = new ServerConfig(host, port);
        return new Server(config);
    }

    public static Response healthCheck() {
        byte[] body = "{\"status\": \"ok\"}".getBytes();
        return new Response(200, body);
    }
}
`

func TestSummarizeRust(t *testing.T) {
	requireAstGrep(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "server.rs")
	if err := os.WriteFile(path, []byte(rustFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	summary, err := codesearch.SummarizeFile(path)
	if err != nil {
		t.Fatalf("SummarizeFile(%s) error: %v", path, err)
	}

	origTokens := estimateTokens(rustFixture)
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
		{"Config struct", "struct Config"},
		{"Router struct", "struct Router"},
		{"Server struct", "struct Server"},
		{"Handler trait", "trait Handler"},
		{"Middleware trait", "trait Middleware"},
		{"HttpMethod enum", "enum HttpMethod"},
		{"create_server func", "func create_server"},
		{"health_check func", "func health_check"},
	}

	for _, c := range checks {
		if !strings.Contains(summary, c.pattern) {
			t.Errorf("summary missing %s (expected %q)\nsummary:\n%s", c.name, c.pattern, summary)
		}
	}

	if !strings.Contains(summary, "L") {
		t.Error("summary has no line numbers")
	}

	t.Logf("summary:\n%s", indent(summary))
}

func TestSummarizeJava(t *testing.T) {
	requireAstGrep(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "Server.java")
	if err := os.WriteFile(path, []byte(javaFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	summary, err := codesearch.SummarizeFile(path)
	if err != nil {
		t.Fatalf("SummarizeFile(%s) error: %v", path, err)
	}

	origTokens := estimateTokens(javaFixture)
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
		{"ServerConfig class", "class ServerConfig"},
		{"Handler interface", "interface Handler"},
		{"Middleware interface", "interface Middleware"},
		{"HttpMethod enum", "enum HttpMethod"},
		{"Request class", "class Request"},
		{"Response class", "class Response"},
		{"Router class", "class Router"},
		{"Server class", "class Server"},
		{"ServerFactory class", "class ServerFactory"},
	}

	for _, c := range checks {
		if !strings.Contains(summary, c.pattern) {
			t.Errorf("summary missing %s (expected %q)\nsummary:\n%s", c.name, c.pattern, summary)
		}
	}

	if !strings.Contains(summary, "L") {
		t.Error("summary has no line numbers")
	}

	t.Logf("summary:\n%s", indent(summary))
}

func TestSummarizeRustNotInstalled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lib.rs")
	if err := os.WriteFile(path, []byte("pub fn hello() {}\n"), 0o600); err != nil {
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

func TestSummarizeJavaNotInstalled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Main.java")
	if err := os.WriteFile(path, []byte("public class Main { public static void main(String[] args) {} }\n"), 0o600); err != nil {
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
