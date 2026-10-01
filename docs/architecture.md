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

![Component Architecture Topology](images/diag_topology.svg)

<details>
<summary><b>View Mermaid Source Code</b></summary>

```mermaid
flowchart TD
    subgraph Clients ["Internal Enterprise Clients"]
        CI["CI/CD Pipeline Runners"]
        Dev["Developer Workstations"]
        Services["Internal Microservices"]
    end

    subgraph Gateway ["GitHub API Gateway (cmd/gateway)"]
        subgraph Pipeline ["HTTP Request Processing Pipeline"]
            Auth["1. Auth Manager<br/>(internal/auth)<br/>Validates pre-shared keys"]
            RL["2. Token-Bucket Rate Limiter<br/>(internal/ratelimit)<br/>60 RPM / 10 Burst per Client"]
            Cache["3. ETag In-Memory Cache<br/>(internal/cache)<br/>Zero-Quota Revalidation"]
            Policy["4. Policy Evaluator<br/>(internal/policy)<br/>Fail-Closed Default Deny"]
            Proxy["5. Reverse Proxy Engine<br/>(internal/proxy)<br/>net/http/httputil"]
        end

        subgraph Management ["Management & Operations"]
            Pool["Largest-Remaining Token Pool<br/>(internal/token)<br/>5000+ Quota Management"]
            Reload["Zero-Restart Reloader<br/>POST /_gateway/reload and SIGHUP"]
            Telemetry["Telemetry Status Handler<br/>GET /_gateway/status"]
            Config["Config Loader<br/>(internal/config)<br/>GITHUB_API_GATEWAY_*"]
        end
    end

    subgraph Upstream ["External GitHub Services"]
        GH["GitHub REST API<br/>api.github.com"]
        S3["GitHub Release CDN<br/>objects.githubusercontent.com"]
    end

    CI -->|HTTP Request + Key| Auth
    Dev -->|HTTP Request + Key| Auth
    Services -->|HTTP Request + Key| Auth
    Auth -->|401 Unauthorized| CI
    Auth -->|Authorized| RL
    RL -->|429 Too Many Requests| CI
    RL -->|Allowed| Cache
    Cache -->|If-None-Match ETag| Policy
    Policy -->|403 Forbidden| CI
    Policy -->|Approved| Proxy

    Pool -->|Inject Largest-Remaining Token| Proxy
    Proxy -->|HTTPS Proxy + Injected Token| GH
    GH -->|Rate Limit Headers| Proxy
    Proxy -->|Record Live Quota| Pool
    GH -->|304 Not Modified| Cache
    Cache -->|Serve 200 OK from Memory| CI
    GH -->|302 Found S3 Redirect| Proxy
    Proxy -->|302 Found Direct Stream| CI
    CI -.->|Stream Binary| S3

    Reload -->|Atomic Pointer Swap| Policy
    Reload -->|Atomic Key Update| Auth
    Reload -->|Update Active Limits| RL
    Reload -->|Preserve Quota States| Pool
```

</details>

---

## 3. Sequence Diagrams

### 3.1 Standard Authorized Request & Token Injection

![Standard Authorized Request Flow](images/diag_seq_authorized.svg)

<details>
<summary><b>View Mermaid Source Code</b></summary>

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
    participant GitHub as "api.github.com"

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
    Pool->>Pool: Select token with Largest Remaining Quota
    Pool-->>Proxy: Return best token (ghp_token)
    Proxy->>Proxy: Inject Authorization: Bearer ghp_token
    Proxy->>GitHub: GET /repos/my-org/backend/releases
    GitHub-->>Proxy: 200 OK (ETag: "v1.0", X-RateLimit-Remaining: 4995)
    Proxy->>Pool: RecordUsage(ghp_token, remaining=4995, resetTime)
    Proxy->>Cache: Intercept 200 OK with ETag
    Cache->>Cache: Store response body and ETag in memory
    Cache-->>Client: 200 OK (JSON body + RateLimit headers)
```

</details>

---

### 3.2 Zero-Quota ETag 304 Revalidation

![ETag 304 Revalidation Flow](images/diag_seq_etag.svg)

<details>
<summary><b>View Mermaid Source Code</b></summary>

```mermaid
sequenceDiagram
    autonumber
    actor Client as Internal Client
    participant Cache as ETag Cache
    participant Policy as Policy Evaluator
    participant Proxy as Reverse Proxy
    participant GitHub as "api.github.com"

    Client->>Cache: GET /repos/my-org/backend/releases
    Cache->>Cache: Cache Hit! Found ETag "v1.0"
    Cache->>Cache: Inject header: If-None-Match "v1.0"
    Cache->>Policy: Forward request
    Policy->>Policy: Validate request against policy
    Policy->>Proxy: Approved
    Proxy->>GitHub: GET /repos/... (If-None-Match: "v1.0")
    GitHub-->>Proxy: 304 Not Modified (0 quota consumed)
    Proxy-->>Cache: 304 Not Modified
    Cache->>Cache: Merge fresh headers with cached body
    Cache->>Cache: Refresh entry timestamp
    Cache-->>Client: 200 OK (Cached JSON body served from RAM)
```

</details>

---

### 3.3 S3 Binary Asset Download Redirect

![S3 Binary Asset Download Redirect Flow](images/diag_seq_s3_redirect.svg)

<details>
<summary><b>View Mermaid Source Code</b></summary>

```mermaid
sequenceDiagram
    autonumber
    actor Client as Internal Client
    participant Gateway as GitHub API Gateway
    participant GitHub as "api.github.com"
    participant S3 as "objects.githubusercontent.com"

    Client->>Gateway: GET /repos/my-org/app/releases/download/v1.0/bundle.tar.gz
    Gateway->>Gateway: Validate Auth, RateLimit and Policy
    Gateway->>GitHub: GET /repos/my-org/app/releases/download/... (Injected Token)
    GitHub-->>Gateway: 302 Found (Location: objects.githubusercontent.com)
    Gateway-->>Client: 302 Found (Location: objects.githubusercontent.com)
    Note over Gateway: Zero memory buffering - large binary not loaded into RAM
    Client->>S3: GET direct binary download
    S3-->>Client: 200 OK (Stream binary asset directly)
```

</details>

---

### 3.4 Zero-Restart Hot-Reloading with Atomic Rollback

![Zero-Restart Hot Reloading Flow](images/diag_seq_hot_reload.svg)

<details>
<summary><b>View Mermaid Source Code</b></summary>

```mermaid
sequenceDiagram
    autonumber
    actor Operator as DevOps / SRE
    participant Server as HTTP Server
    participant Reloader as "App.Reload()"
    participant State as "Gateway State"

    Operator->>Server: POST /_gateway/reload (or SIGHUP)
    Server->>Reloader: Invoke Reload()
    Reloader->>Reloader: Reload .env and environment variables
    Reloader->>Reloader: Parse and Validate policy.yaml

    alt Invalid YAML Syntax or Misspelled Method
        Reloader->>Reloader: Validation Failed
        Reloader->>Reloader: Roll back and keep previous state pointers intact
        Reloader-->>Server: Error (validation failed)
        Server-->>Operator: 400 Bad Request ("previous configuration preserved")
    else Valid Configuration
        Reloader->>State: Atomically update Policy rules
        Reloader->>State: Atomically replace allowed Gateway Keys
        Reloader->>State: Update Token Pool (preserves existing quota status)
        Reloader->>State: Dynamically update Rate Limits
        Reloader-->>Server: Success
        Server-->>Operator: 200 OK (status: reloaded)
    end
```

</details>

---

## 4. State Machines

### 4.1 Token Pool Lifecycle & Reset Probe Cooldown

![Token Pool State Lifecycle](images/diag_state_token_pool.svg)

<details>
<summary><b>View Mermaid Source Code</b></summary>

```mermaid
stateDiagram-v2
    [*] --> Healthy: Initialize Token (Quota = 5000)

    state Healthy {
        [*] --> InPool
        InPool --> InPool: Consume Quota (Remaining at least 3000)
    }

    Healthy --> LowQuotaReserve: Remaining below TOKEN_MIN_REMAINING (3000)

    state LowQuotaReserve {
        [*] --> Sidelined: Excluded from active selection
        Sidelined --> ReadyToProbe: Time past ResetTime plus 10s Buffer
    }

    LowQuotaReserve --> ProbeRequest: Send client request inline

    state ProbeRequest {
        [*] --> EvaluatingUpstream
    }

    ProbeRequest --> Healthy: GitHub rolled over (Remaining = 4999)
    ProbeRequest --> ProbeCooldown: Rollover delayed (Remaining below 3000)

    state ProbeCooldown {
        [*] --> Throttled: Throttle further probes for 10 seconds
        Throttled --> LowQuotaReserve: Cooldown expired
    }

    Healthy --> AllTokensExhausted: All tokens below 3000 and no probe ready
    LowQuotaReserve --> AllTokensExhausted: No tokens eligible

    state AllTokensExhausted {
        [*] --> CheckFallback
        CheckFallback --> FallbackUnauthenticated: FALLBACK_UNAUTHENTICATED is true
        CheckFallback --> Error429: FALLBACK_UNAUTHENTICATED is false
    }
```

</details>

---

### 4.2 Per-Client Token-Bucket Rate Limiter

![Rate Limiter State Machine](images/diag_state_rate_limiter.svg)

<details>
<summary><b>View Mermaid Source Code</b></summary>

```mermaid
stateDiagram-v2
    [*] --> FullBurst: New Client (tokens = Burst, e.g. 10)

    state FullBurst {
        [*] --> Idle
        Idle --> Consuming: Request arrives
    }

    Consuming --> Allowed: Tokens at least 1.0 (Deduct 1.0)
    Allowed --> FullBurst: Return 200 OK and Proceed

    Consuming --> Exhausted: Tokens below 1.0
    Exhausted --> Throttled: Return 429 Too Many Requests with Retry-After header

    state Throttled {
        [*] --> WaitingForRefill
        WaitingForRefill --> Refilling: Time advances (tokens refill at rate)
    }

    Refilling --> FullBurst: Tokens at least 1.0 (Clamped to Burst max)
```

</details>

---

## 5. Class & Structural Relationships

![Class Structure and Composition](images/diag_class_diagram.svg)

<details>
<summary><b>View Mermaid Source Code</b></summary>

```mermaid
classDiagram
    class App {
        -Config config
        -Policy policy
        -Pool pool
        -Cache cache
        -Manager authMgr
        -Limiter limiter
        -Handler proxy
        -Handler httpHandler
        -RWMutex mu
        +Reload() ReloadResponse
    }

    class Config {
        +string Port
        +List~string~ GitHubTokens
        +List~string~ AuthKeys
        +string PolicyPath
        +int TokenMinRemaining
        +bool FallbackUnauthenticated
        +int CacheTTLSeconds
        +string TargetURL
        +int ClientMaxRPM
        +int ClientBurst
    }

    class Manager {
        -Map keySet
        -RWMutex mu
        +SetKeys(keys)
        +KeyCount() int
        +Middleware(next) Handler
    }

    class Limiter {
        -Map buckets
        -int maxRPM
        -int burst
        -float ratePerS
        -RWMutex mu
        +GetBucket(clientID) Bucket
        +Allow(clientID) bool
        +AllowAt(clientID, now) bool
        +UpdateLimits(maxRPM, burst)
        +Middleware(next) Handler
    }

    class Bucket {
        -float tokens
        -Time lastRefill
        -int burst
        -float ratePerS
        -Mutex mu
        +Allow() bool
        +AllowAt(now) bool
    }

    class Cache {
        -Map entries
        -Duration ttl
        -RWMutex mu
        +Get(key) Entry
        +Set(key, entry)
        +Count() int
        +Middleware(next) Handler
    }

    class Policy {
        -List rules
        +Evaluate(method, path) bool
        +RuleCount() int
    }

    class Pool {
        -List tokens
        -int minRemaining
        -bool fallbackUnauthenticated
        -RWMutex mu
        +Acquire() string
        +RecordUsage(token, remaining, reset)
        +UpdateTokens(tokens)
        +Status() List
    }

    class Handler {
        -URL target
        -Policy policy
        -Pool pool
        -RWMutex policyMu
        +SetPolicy(pol)
        +ServeHTTP(w, r)
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

</details>
