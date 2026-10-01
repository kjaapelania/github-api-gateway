# Architecture & UML Specification: GitHub API Gateway

This document provides technical design specifications and unified modeling language (UML) diagrams for the **GitHub API Gateway**.

![Architecture Infographic](images/architecture_infographic.png)

---

## 1. System Boundary & Deployment Scope

> [!CAUTION]
> **Internal Private Network Scope Only**
> The GitHub API Gateway is designed strictly for **internal enterprise VPCs, private company subnets, or department clusters**. It transparently injects company-owned GitHub Personal Access Tokens and caches responses in memory.
>
> **NEVER expose this gateway to the public internet.** Exposing it publicly would allow unauthorized external actors to exhaust your pooled GitHub token quota, trigger global IP bans, or abuse the proxy.

```
+--------------------------------------------------------------------------------+
| PRIVATE COMPANY NETWORK / VPC (Firewalled Subnet)                              |
|                                                                                |
|  [ CI/CD Runners ]     [ Dev Workstations ]     [ Internal Microservices ]     |
|          \                      |                      /                       |
|           \                     |                     /                        |
|            +--------------------+--------------------+                         |
|                                 |                                              |
|                                 v                                              |
|                  +------------------------------+                              |
|                  |    GitHub API Gateway        |                              |
|                  |  - Pre-Shared Key Auth       |                              |
|                  |  - Token-Bucket Rate Limiter |                              |
|                  |  - ETag Zero-Quota Cache     |                              |
|                  |  - Fail-Closed Policy Guard  |                              |
|                  |  - Multi-Token Pool          |                              |
|                  |  - Zero-Restart Hot-Reload   |                              |
|                  +--------------+---------------+                              |
+---------------------------------|----------------------------------------------+
                                  | (HTTPS via NAT Gateway / Egress Proxy)
==================================|===============================================
PUBLIC INTERNET                   v
                 +---------------------------------+
                 |  GitHub REST API (api.github.com)|  (5,000+ req/hr per token)
                 +----------------+----------------+
                                  |
                                  | (302 Found Asset Redirect)
                                  v
                 +---------------------------------+
                 |  AWS S3 CDN (objects.github...) |  (Zero bandwidth gateway cost)
                 +---------------------------------+
```

---

## 2. Component Architecture

The gateway is built using pure Go standard library primitives with zero runtime external dependencies (`gopkg.in/yaml.v3` is authorized exclusively for policy parsing).

```mermaid
flowchart TD
    subgraph Clients["Internal Enterprise Clients"]
        CI["CI/CD Pipeline Runners"]
        Dev["Developer Workstations"]
        Services["Internal Microservices"]
    end

    subgraph Gateway["GitHub API Gateway (cmd/gateway)"]
        subgraph Pipeline["HTTP Request Processing Pipeline"]
            Auth["1. Auth Manager<br/>(internal/auth)<br/>Validates pre-shared keys"]
            RL["2. Token-Bucket Rate Limiter<br/>(internal/ratelimit)<br/>60 RPM / 10 Burst per Client"]
            Cache["3. ETag In-Memory Cache<br/>(internal/cache)<br/>Zero-Quota Revalidation"]
            Policy["4. Policy Evaluator<br/>(internal/policy)<br/>Fail-Closed Default Deny"]
            Proxy["5. Reverse Proxy Engine<br/>(internal/proxy)<br/>net/http/httputil"]
        end

        subgraph Management["Management & Operations"]
            Pool["Largest-Remaining Token Pool<br/>(internal/token)<br/>5000+ Quota Management"]
            Reload["Zero-Restart Reloader<br/>POST /_gateway/reload & SIGHUP"]
            Telemetry["Telemetry Status Handler<br/>GET /_gateway/status"]
            Config["Config Loader<br/>(internal/config)<br/>GITHUB_API_GATEWAY_*"]
        end
    end

    subgraph Upstream["External GitHub Services"]
        GH["GitHub REST API<br/>api.github.com"]
        S3["GitHub Release CDN<br/>objects.githubusercontent.com"]
    end

    Clients -->|"HTTP Request + Gateway Key"| Auth
    Auth -->|"401 Unauthorized (Invalid Key)"| Clients
    Auth -->|"Authorized"| RL
    RL -->|"429 Too Many Requests (Retry-After)"| Clients
    RL -->|"Allowed"| Cache
    Cache -->|"If-None-Match ETag injected"| Policy
    Policy -->|"403 Forbidden (Default Deny / Sensitive)"| Clients
    Policy -->|"Approved"| Proxy

    Pool -->|"Inject Largest-Remaining Token"| Proxy
    Proxy -->|"HTTPS Proxy + Injected Token"| GH
    GH -->|"Rate Limit Headers (Remaining, Reset)"| Proxy
    Proxy -->|"Record Live Quota"| Pool
    GH -->|"304 Not Modified"| Cache
    Cache -->|"Serve 200 OK from Memory"| Clients
    GH -->|"302 Found S3 Redirect"| Proxy
    Proxy -->|"302 Found (Direct Stream)"| Clients
    Clients -.->|"Stream Binary"| S3

    Reload -->|"Atomic Pointer Swap"| Policy
    Reload -->|"Atomic Key Update"| Auth
    Reload -->|"Update Active Limits"| RL
    Reload -->|"Preserve Quota States"| Pool
```

---

## 3. Sequence Diagrams

### 3.1 Standard Authorized Request & Token Injection

```mermaid
sequenceDiagram
    autonumber
    actor Client as Internal Client
    participant Auth as Auth Manager
    participant RL as Rate Limiter
    participant Cache as ETag Cache
    participant Policy as Policy Evaluator
    participant Pool as Token Pool
    participant Proxy as Reverse Proxy
    participant GitHub as api.github.com

    Client->>Auth: GET /repos/my-org/backend/releases (X-Gateway-Key: ci-token)
    Auth->>Auth: Validate client key in allowed set
    Auth->>RL: Forward request
    RL->>RL: Check client bucket (Tokens >= 1.0)
    RL->>RL: Deduct 1.0 token
    RL->>Cache: Forward request
    Cache->>Cache: Check URI in cache (Miss)
    Cache->>Policy: Forward request
    Policy->>Policy: Check Denylist (/user, /admin, etc.)
    Policy->>Policy: Check Policy Rules (repo glob, path glob, methods)
    Policy-->>Proxy: Approved
    Proxy->>Pool: Acquire()
    Pool->>Pool: Select token with Largest Remaining Quota (>= 3000)
    Pool-->>Proxy: Return best token (ghp_***)
    Proxy->>Proxy: Inject Authorization: Bearer ghp_***
    Proxy->>GitHub: GET https://api.github.com/repos/my-org/backend/releases
    GitHub-->>Proxy: 200 OK (ETag: "v1.0", X-RateLimit-Remaining: 4995)
    Proxy->>Pool: RecordUsage(ghp_***, remaining=4995, resetTime)
    Proxy->>Cache: Intercept 200 OK with ETag
    Cache->>Cache: Store response body & ETag in memory
    Cache-->>Client: 200 OK (JSON body + RateLimit headers)
```

---

### 3.2 Zero-Quota ETag 304 Revalidation

```mermaid
sequenceDiagram
    autonumber
    actor Client as Internal Client
    participant Cache as ETag Cache
    participant Policy as Policy Evaluator
    participant Proxy as Reverse Proxy
    participant GitHub as api.github.com

    Client->>Cache: GET /repos/my-org/backend/releases
    Cache->>Cache: Cache Hit! Found ETag "v1.0"
    Cache->>Cache: Inject header: If-None-Match: "v1.0"
    Cache->>Policy: Forward request
    Policy->>Policy: Validate request against policy
    Policy->>Proxy: Approved
    Proxy->>GitHub: GET /repos/... (If-None-Match: "v1.0")
    GitHub-->>Proxy: 304 Not Modified (0 quota consumed!)
    Proxy-->>Cache: 304 Not Modified
    Cache->>Cache: Merge fresh headers with cached body
    Cache->>Cache: Refresh entry timestamp
    Cache-->>Client: 200 OK (Cached JSON body served from RAM)
```

---

### 3.3 S3 Binary Asset Download Redirect

```mermaid
sequenceDiagram
    autonumber
    actor Client as Internal Client
    participant Gateway as GitHub API Gateway
    participant GitHub as api.github.com
    participant S3 as objects.githubusercontent.com

    Client->>Gateway: GET /repos/my-org/app/releases/download/v1.0/bundle.tar.gz
    Gateway->>Gateway: Validate Auth, RateLimit & Policy
    Gateway->>GitHub: GET /repos/my-org/app/releases/download/... (Injected Token)
    GitHub-->>Gateway: 302 Found (Location: https://objects.githubusercontent.com/...)
    Gateway-->>Client: 302 Found (Location: https://objects.githubusercontent.com/...)
    Note over Gateway: Zero memory buffering; large binary not loaded into RAM
    Client->>S3: GET https://objects.githubusercontent.com/...
    S3-->>Client: 200 OK (Stream binary asset directly)
```

---

### 3.4 Zero-Restart Hot-Reloading with Atomic Rollback

```mermaid
sequenceDiagram
    autonumber
    actor Operator as DevOps / SRE
    participant Server as HTTP Server
    participant Reloader as App.Reload()
    participant State as Gateway State (Policy, Pool, Auth, Limiter)

    Operator->>Server: POST /_gateway/reload (or SIGHUP)
    Server->>Reloader: Invoke Reload()
    Reloader->>Reloader: Reload .env & environment variables
    Reloader->>Reloader: Parse & Validate policy.yaml

    alt Invalid YAML Syntax or Misspelled Method
        Reloader->>Reloader: Validation Failed!
        Reloader->>Reloader: Roll back; keep previous state pointers intact
        Reloader-->>Server: Error (validation failed)
        Server-->>Operator: 400 Bad Request ("previous configuration preserved")
    else Valid Configuration
        Reloader->>State: Atomically update Policy rules
        Reloader->>State: Atomically replace allowed Gateway Keys
        Reloader->>State: Update Token Pool (preserves existing quota status)
        Reloader->>State: Dynamically update Rate Limits (UpdateLimits)
        Reloader-->>Server: Success
        Server-->>Operator: 200 OK ({"status":"reloaded", "active_tokens":...})
    end
```

---

## 4. State Machines

### 4.1 Token Pool Lifecycle & Reset Probe Cooldown

```mermaid
stateDiagram-v2
    [*] --> Healthy: Initialize Token (Quota = 5000)

    state Healthy {
        [*] --> InPool
        InPool --> InPool: Consume Quota (Remaining >= 3000)
    }

    Healthy --> LowQuotaReserve: Remaining < TOKEN_MIN_REMAINING (3000)

    state LowQuotaReserve {
        [*] --> Sidelined: Excluded from active selection
        Sidelined --> ReadyToProbe: Now > ResetTime + 10s Buffer
    }

    LowQuotaReserve --> ProbeRequest: Send client request inline

    state ProbeRequest {
        [*] --> EvaluatingUpstream
    }

    ProbeRequest --> Healthy: GitHub rolled over (Remaining = 4999)
    ProbeRequest --> ProbeCooldown: Rollover delayed (Remaining < 3000)

    state ProbeCooldown {
        [*] --> Throttled: Throttle further probes for 10 seconds
        Throttled --> LowQuotaReserve: Cooldown expired
    }

    Healthy --> AllTokensExhausted: All tokens < 3000 & no probe ready
    LowQuotaReserve --> AllTokensExhausted: No tokens eligible

    state AllTokensExhausted {
        [*] --> CheckFallback
        CheckFallback --> FallbackUnauthenticated: FALLBACK_UNAUTHENTICATED=true
        CheckFallback --> Error429: FALLBACK_UNAUTHENTICATED=false
    }
```

---

### 4.2 Per-Client Token-Bucket Rate Limiter

```mermaid
stateDiagram-v2
    [*] --> FullBurst: New Client (tokens = Burst, e.g. 10)

    state FullBurst {
        [*] --> Idle
        Idle --> Consuming: Request arrives
    }

    Consuming --> Allowed: Tokens >= 1.0 (Deduct 1.0)
    Allowed --> FullBurst: Return 200 OK / Proceed

    Consuming --> Exhausted: Tokens < 1.0
    Exhausted --> Throttled: Return 429 Too Many Requests (Retry-After: N)

    state Throttled {
        [*] --> WaitingForRefill
        WaitingForRefill --> Refilling: Time advances (tokens += elapsed * rate)
    }

    Refilling --> FullBurst: Tokens >= 1.0 (Clamped to Burst max)
```

---

## 5. Class & Structural Relationships

```mermaid
classDiagram
    class App {
        -Config *Config
        -Policy *Policy
        -Pool *Pool
        -Cache *Cache
        -AuthMgr *Manager
        -Limiter *Limiter
        -Proxy *Handler
        -Handler http.Handler
        -mu sync.RWMutex
        +Reload() *ReloadResponse
    }

    class Config {
        +Port string
        +GitHubTokens []string
        +AuthKeys []string
        +PolicyPath string
        +TokenMinRemaining int
        +FallbackUnauthenticated bool
        +CacheTTLSeconds int
        +TargetURL string
        +ClientMaxRPM int
        +ClientBurst int
    }

    class Manager {
        -keySet map[string]bool
        -mu sync.RWMutex
        +SetKeys(keys []string)
        +KeyCount() int
        +Middleware(next http.Handler) http.Handler
    }

    class Limiter {
        -buckets map[string]*Bucket
        -maxRPM int
        -burst int
        -ratePerS float64
        -mu sync.RWMutex
        +GetBucket(clientID string) *Bucket
        +Allow(clientID string) (bool, int)
        +AllowAt(clientID string, now time.Time) (bool, int)
        +UpdateLimits(maxRPM int, burst int)
        +Middleware(next http.Handler) http.Handler
    }

    class Bucket {
        -tokens float64
        -lastRefill time.Time
        -burst int
        -ratePerS float64
        -mu sync.Mutex
        +Allow() (bool, int)
        +AllowAt(now time.Time) (bool, int)
    }

    class Cache {
        -entries map[string]*Entry
        -ttl time.Duration
        -mu sync.RWMutex
        +Get(key string) (*Entry, bool)
        +Set(key string, entry *Entry)
        +Count() int
        +Middleware(next http.Handler) http.Handler
    }

    class Policy {
        -rules []Rule
        +Evaluate(method string, path string) (bool, DenialReason)
        +RuleCount() int
    }

    class Pool {
        -tokens []*TokenStatus
        -minRemaining int
        -fallbackUnauthenticated bool
        -mu sync.RWMutex
        +Acquire() (string, error)
        +RecordUsage(token string, remaining int, reset time.Time)
        +UpdateTokens(tokens []string)
        +Status() []TokenInfo
    }

    class Handler {
        -target *url.URL
        -policy *Policy
        -pool *Pool
        -policyMu sync.RWMutex
        +SetPolicy(pol *Policy)
        +ServeHTTP(w http.ResponseWriter, r *http.Request)
    }

    App *-- Config
    App *-- Manager
    App *-- Limiter
    App *-- Cache
    App *-- Policy
    App *-- Pool
    App *-- Handler
    Limiter *-- Bucket
```
