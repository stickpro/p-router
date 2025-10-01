# Proxy Router

A high-performance HTTP/HTTPS proxy router written in Go that allows you to manage multiple proxy servers through a single endpoint with authentication-based routing.

## How It Works
### Request Flow Diagram
```mermaid
sequenceDiagram
participant Client
participant ProxyRouter as Proxy Router<br/>(localhost:8080)
participant Cache as Router Cache
participant DB as SQLite DB
participant TargetProxy as Target Proxy<br/>(176.109.145.175:2376)
participant Internet as Internet<br/>(example.com)

    Client->>ProxyRouter: 1. HTTP/HTTPS Request<br/>Proxy-Authorization: Basic proxy1:pass1
    ProxyRouter->>ProxyRouter: 2. Parse credentials
    ProxyRouter->>Cache: 3. Lookup proxy1:pass1
    
    alt Cache Hit
        Cache-->>ProxyRouter: Target: 176.109.145.175:2376
    else Cache Miss
        Cache->>DB: Query proxy by username
        DB-->>Cache: Return proxy config
        Cache-->>ProxyRouter: Target: 176.109.145.175:2376
    end
    
    ProxyRouter->>TargetProxy: 4. Forward request
    TargetProxy->>Internet: 5. Request resource
    Internet-->>TargetProxy: 6. Response data
    TargetProxy-->>ProxyRouter: 7. Response data
    ProxyRouter-->>Client: 8. Final response
```

## Architecture Diagram
```mermaid
graph TB
    subgraph "Client Layer"
        A[Client Application]
    end
    
    subgraph "Proxy Router Service"
        B[HTTP Server<br/>:8080]
        C[Router<br/>Authentication & Routing]
        D[In-Memory Cache]
        E[Repository<br/>Data Access]
        F[(SQLite DB<br/>proxies.db)]
    end
    
    subgraph "Target Proxies"
        G1[Proxy 1<br/>176.109.145.175:2376]
        G2[Proxy 2<br/>85.159.2.31:58080]
        G3[Proxy 3<br/>171.7.50.174:8899]
        G4[Proxy 4+5<br/>...]
    end
    
    subgraph "Internet"
        H[Destination<br/>example.com]
    end
    
    A -->|HTTP/HTTPS + Auth| B
    B -->|Validate & Route| C
    C -->|Check Cache| D
    D -.->|Cache Miss| E
    E -.->|Query| F
    F -.->|Proxy Config| E
    E -.->|Load to Cache| D
    C -->|Forward Request| G1
    C -->|Forward Request| G2
    C -->|Forward Request| G3
    C -->|Forward Request| G4
    G1 -->|Fetch| H
    G2 -->|Fetch| H
    G3 -->|Fetch| H
    G4 -->|Fetch| H
    
    style A fill:#e1f5ff
    style B fill:#fff4e1
    style C fill:#fff4e1
    style D fill:#f0f0f0
    style E fill:#fff4e1
    style F fill:#e8f5e9
    style G1 fill:#fce4ec
    style G2 fill:#fce4ec
    style G3 fill:#fce4ec
    style G4 fill:#fce4ec
    style H fill:#f3e5f5
```

## Features

- 🔐 **Authentication-based routing** - Route requests to different proxies based on username/password
- 🚀 **HTTP & HTTPS support** - Full support for both HTTP and HTTPS (CONNECT tunneling)
- 💾 **SQLite storage** - Persistent proxy configuration with SQLite database
- ⚡ **In-memory caching** - Fast proxy lookup with automatic caching
- 🏗️ **Clean architecture** - Repository pattern for easy testing and extensibility
- 🔄 **Thread-safe** - Concurrent request handling with mutex protection
- 📊 **CRUD operations** - Easy proxy management (Create, Read, Update, Delete)

## Installation


## Usage

### Quick Start

```bash
# Run the application
make run start
```

The server will start on `localhost:8080` and create a `proxies.db` SQLite database with sample proxies.

### Import proxy
```bash
# import proxy
make build
./.bin/proxy-router import --file ./proxies.txt
```


## Performance

- **In-memory caching** ensures fast proxy lookup (O(1) complexity)
- **Connection pooling** for database queries
- **Concurrent request handling** with goroutines
- **Thread-safe operations** with mutex locks

## Testing

```bash
# Run all tests
go test ./...

# Run tests with coverage
go test -cover ./...

# Run tests with race detection
go test -race ./...
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## Roadmap
- [x] Check aliveness of target proxies
- [x] Automatic removal of dead proxies
- [x] Configuration file support (YAML/JSON)
- [ ] REST API for proxy management
- [ ] Web UI dashboard
- [ ] Load balancing between multiple proxies
- [ ] Request/response logging
- [ ] Statistics and metrics
- [ ] Support for SOCKS5 protocol
- [ ] Docker support
- [ ] Rate limiting per user

## License

MIT License - see the [LICENSE](LICENSE) file for details

## Acknowledgments

- Built with [go-sqlite3](https://github.com/mattn/go-sqlite3)
- Inspired by various proxy rotation tools

## Support

If you have any questions or issues, please open an issue on GitHub.
