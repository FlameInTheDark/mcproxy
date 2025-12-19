# MCP Proxy

A lightweight proxy server for the Model Context Protocol (MCP). This application allows you to expose multiple MCP servers (running locally via Stdio or remotely via SSE) through a single HTTP/SSE interface.

## Overview

The MCP Proxy acts as a bridge between MCP clients and upstream MCP servers. It handles:

1.  **Stdio Integration**: Spawns and manages local CLI-based MCP servers (e.g., file system servers, git integration) and communicates with them via standard input/output.
2.  **SSE Integration**: Connects to existing remote MCP servers via Server-Sent Events (SSE).
3.  **Unified Interface**: Exposes each configured upstream server via a consistent HTTP/SSE endpoint.
4.  **Logging**: Provides structured logging of JSON-RPC requests and responses for debugging and monitoring.

## Configuration

The application reads a configuration file (default: `mcp-proxy.yaml` in the current directory) to determine which upstream servers to manage.

### Example mcp-proxy.yaml

```yaml
mcpServers:
  - name: "filesystem"
    type: "stdio"
    command: "npx"
    args: 
      - "-y"
      - "@modelcontextprotocol/server-filesystem"
      - "."
    env:
      - "NODE_ENV=production"

  - name: "remote-tool"
    type: "http" # or "sse"
    url: "http://example.com:8000/sse"

server:
  port: 8080
  transport: "http" # or "stdio"
```

### Configuration Options

*   **mcpServers**: A list of upstream servers.
    *   **name**: Unique identifier for the server. Used in the proxy URL.
    *   **type**: `stdio`, `sse`, or `http` (alias for sse).
    *   **command**: (Stdio only) The executable to run.
    *   **args**: (Stdio only) Arguments to pass to the command.
    *   **env**: (Stdio only) Environment variables (KEY=VALUE).
    *   **url**: (SSE/HTTP only) The URL of the upstream SSE endpoint.
*   **server**: Global server settings.
    *   **port**: The TCP port the proxy listens on (used when transport is `http`).
    *   **transport**: The mode of operation for the proxy.
        *   `http` (default): Runs an HTTP server exposing SSE endpoints.
        *   `stdio`: Runs as a command-line tool, communicating via Standard Input/Output. This allows the proxy itself to be used as a "tool" by other MCP clients (like Claude Desktop).

## Usage

1.  Build the application:
    ```bash
    go build -o mcproxy cmd/mcproxy/main.go
    ```

2.  Create a `mcp-proxy.yaml` file in your working directory (or specify one via flags).

3.  Run the proxy:
    ```bash
    ./mcproxy
    ```
    Or with a custom config path:
    ```bash
    ./mcproxy -config /path/to/my-config.yaml
    ```

## Connecting Clients

Once the proxy is running, you can connect MCP clients to the configured upstreams using the following URL pattern:

```
http://<proxy-host>:<port>/mcp/<server-name>/sse
```

For example, if you defined a server named `filesystem` and the proxy is running on port 8080:

```
http://localhost:8080/mcp/filesystem/sse
```

## Aggregated Endpoint

The proxy also exposes a unified endpoint that aggregates all configured tools into a single MCP server. This allows clients to see and use all tools from all upstreams simultaneously.

```
http://<proxy-host>:<port>/sse
```

**Note:** In aggregated mode, the proxy intercepts `initialize` and `tools/list` requests to merge capabilities and tool lists from all upstreams.

## Architecture

*   **cmd/mcproxy**: Application entry point.
*   **internal/config**: Configuration loading logic.
*   **internal/mcp**: MCP JSON-RPC type definitions.
*   **internal/upstream**: Implementations for Stdio and SSE upstream clients.
*   **internal/transport**: HTTP server handling SSE connections and message routing.

This project follows the standard Go project layout.
