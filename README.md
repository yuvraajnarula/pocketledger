## Architecture & Design

PocketLedger is designed around correctness-first payment principles. The architecture enforces strict boundaries between pure business logic and infrastructure side-effects.

### High-Level System Architecture
```mermaid
flowchart TD
    subgraph Clients
        Dashboard[Web Dashboard]
        API[API Consumers / cURL]
    end

    subgraph Edge Layer
        Nginx[Nginx Gateway\nLoad Balancer & Rate Limiter]
    end

    subgraph PocketLedger Cluster
        subgraph Microservices
            WalletSvc[Wallet Service\n:8081\nLifecycle & Reads]
            LedgerSvc[Ledger Service\n:8082\nMoney Movement & Writes]
        end
        
        subgraph Shared Core
            Domain[shared/domain\nPure Business Logic\nZero Dependencies]
        end

        subgraph Data Layer
            DB[(PostgreSQL\nDouble-Entry Ledger\nIdempotency Keys)]
        end
    end

    subgraph Observability
        Prom[(Prometheus)]
        Graf[(Grafana)]
    end

    Dashboard -->|HTTP/JSON| Nginx
    API -->|HTTP/JSON| Nginx
    
    Nginx -->|mTLS + /wallets/*| WalletSvc
    Nginx -->|mTLS + /wallets/*/transfer| LedgerSvc
    
    WalletSvc -->|Uses| Domain
    LedgerSvc -->|Uses| Domain
    
    WalletSvc -->|Reads/Writes| DB
    LedgerSvc -->|ACID Transactions + Idempotency| DB
    
    WalletSvc -->|mTLS| LedgerSvc
    LedgerSvc -->|mTLS| WalletSvc
    
    Prom -->|Scrapes /metrics| WalletSvc
    Prom -->|Scrapes /metrics| LedgerSvc
    Graf -->|Queries| Prom

    classDef service fill:#3b82f6,stroke:#1e40af,color:#fff
    classDef data fill:#10b981,stroke:#065f46,color:#fff
    classDef edge fill:#f59e0b,stroke:#b45309,color:#fff
    
    class WalletSvc,LedgerSvc service
    class DB data
    class Nginx edge
```
### Low Level
```mermaid
flowchart LR
    subgraph Imperative Shell [Side Effects & I/O]
        HTTP[HTTP Handlers\nchi/stdlib mux]
        MW[Middleware\nIdempotency, Chaos, Metrics]
        Repo[(Infrastructure\nSQLite/Postgres Repos)]
    end

    subgraph Functional Core [Pure Logic]
        App[Application Services\nOrchestration & TX Boundaries]
        Domain[Domain Models\nWallet, LedgerEntry, Errors]
        Math[Ledger Math\nComputeTransfer, Validate]
    end

    HTTP -->|Calls| App
    MW -->|Wraps| HTTP
    App -->|Calls| Math
    App -->|Uses| Domain
    App -->|Calls| Repo
    
    Repo -.->|Implements Interfaces| App
    
    Math -->|Returns| Domain
    Domain -.->|No Imports| Math

    style Functional Core fill:#f3f4f6,stroke:#4b5563,stroke-width:2px
    style Imperative Shell fill:#e0f2fe,stroke:#0284c7,stroke-width:2px
```
### Idempotent Transfer Flow
This sequence diagram illustrates how the system prevents double-charges on network retries and handles race conditions using optimistic locking instead of pessimistic table locks.
```mermaid
sequenceDiagram
    autonumber
    actor Client
    participant Nginx as Nginx Gateway
    participant Ledger as Ledger Service
    participant DB as PostgreSQL
    participant Evt as Event Log (optional)

    Client->>Nginx: POST /transfer (Idempotency-Key: key-123)
    Nginx->>Ledger: Forward Request (mTLS)
    
    Ledger->>DB: BEGIN TRANSACTION
    
    Ledger->>DB: SELECT status FROM idempotency_keys WHERE key = key-123
    
    alt Key COMPLETED
        DB-->>Ledger: status = 'COMPLETED'
        Ledger-->>Nginx: Return cached response
    else Key PENDING (concurrent)
        DB-->>Ledger: status = 'PENDING'
        Ledger-->>Nginx: 409 Conflict
    else Key NEW
        DB-->>Ledger: not found
        Ledger->>DB: INSERT INTO idempotency_keys (key, status, expires_at) VALUES ('key-123', 'PENDING', NOW() + INTERVAL '24 hours') ON CONFLICT DO NOTHING
        alt Insert fails (concurrent)
            DB-->>Ledger: conflict
            Ledger-->>Nginx: 409 Conflict
        else Insert succeeds
            Ledger->>DB: SELECT balance, version FROM wallets WHERE id=src FOR UPDATE
            DB-->>Ledger: balance=100, version=5
            Ledger->>DB: UPDATE wallets SET balance=balance-10, version=6 WHERE id=src AND version=5
            alt Version mismatch
                DB-->>Ledger: 0 rows affected
                Ledger->>DB: ROLLBACK
                Ledger->>DB: UPDATE idempotency_keys SET status='FAILED' WHERE key='key-123'
                Ledger-->>Nginx: 409 Optimistic Lock Fail
            else Success
                DB-->>Ledger: 1 row affected
                Ledger->>DB: INSERT ledger_entries (DEBIT src, CREDIT dest)
                Ledger->>Evt: Append Event (optional)
                Ledger->>DB: COMMIT
                Ledger->>DB: UPDATE idempotency_keys SET status='COMPLETED', response_code=200, response_body='...' WHERE key='key-123'
                Ledger-->>Nginx: 200 OK
            end
        end
    end
    
    Nginx-->>Client: Final Response
```
### Data Model
```mermaid
erDiagram
    WALLETS {
        string id PK "UUID"
        string currency "ISO 4217"
        bigint balance "Computed / Cached"
        int version "Optimistic Lock"
        datetime created_at
    }

    LEDGER_ENTRIES {
        string id PK "UUID"
        string wallet_id FK
        string entry_type "DEBIT | CREDIT"
        bigint amount "Always positive"
        string reference_id "Idempotency/TX Ref"
        datetime created_at
    }

    IDEMPOTENCY_KEYS {
        string key PK "Client-provided"
        string status "PENDING | COMPLETED | FAILED"
        int response_code
        json response_body
        datetime expires_at
    }

    EVENT_LOG {
        bigint id PK "Auto-increment"
        string aggregate_id FK "Wallet ID"
        string event_type "wallet.transfer"
        json payload
        datetime created_at
    }

    WALLETS ||--o{ LEDGER_ENTRIES : "has"
    WALLETS ||--o{ EVENT_LOG : "generates"
    
    %% Double Entry Invariant: SUM(Credits) - SUM(Debits) == SUM(Wallet Balances)
```
