# Cómo funciona el servicio

Diagramas del Booking Confirmation Service. Los ejemplos usan datos de una
corrida real: reserva `1121231`, código `vqibl`.

---

## 1. Los dos procesos

Son dos binarios separados a propósito: el dispositivo del huésped **no** es
parte del servidor, y eso es lo que permite que esté offline.

```mermaid
flowchart LR
    subgraph cliente["Cliente"]
        C["POST /bookings"]
    end

    subgraph server["Proceso 1 — cmd/server (:3000)"]
        direction TB
        H["handler<br/>chi + validación"]
        SV["service<br/>lógica de negocio"]
        R["repository<br/>memoria + snapshot"]
        W["delivery worker<br/>ticker cada 500ms"]
        CL["client<br/>webhook HTTP"]
        FILE[("data/store.json")]

        H --> SV
        SV --> R
        W --> SV
        SV --> CL
        R <--> FILE
    end

    subgraph guest["Proceso 2 — cmd/guest (:4000)"]
        direction TB
        G["POST /notifications"]
        SEEN["set de códigos vistos<br/>deduplicación"]
        G --> SEEN
    end

    C --> H
    CL -->|"POST /notifications"| G
    SEEN -->|"POST /acks"| H

    style FILE fill:#e8e8e8,stroke:#888
```

El código **nunca** vuelve por donde entró: sale por el webhook.

---

## 2. Camino feliz

```mermaid
sequenceDiagram
    autonumber
    actor Cli as Cliente
    participant API as Server :3000
    participant DB as store.json
    participant WK as Worker
    participant Dev as Dispositivo :4000
    actor Hue as Huésped

    Cli->>API: POST /bookings<br/>{reservationId: 1121231}
    API->>API: genera código "vqibl"<br/>crypto/rand
    API->>DB: booking + delivery<br/>en UNA operación atómica
    API-->>Cli: 202 Accepted<br/>{status: accepted}

    Note over Cli,API: La respuesta NO trae el código.<br/>El cliente nunca lo ve.

    WK->>DB: Due(now) → entregas vencidas
    WK->>Dev: POST /notifications<br/>{confirmationCode: vqibl}
    Dev-->>WK: 200 OK
    Dev->>Hue: 📱 muestra "vqibl"
    WK->>DB: RecordAttempt<br/>attempts=1, agenda el próximo

    Note over WK,Dev: El 200 no cerró nada.<br/>El próximo intento queda agendado.

    Dev->>API: POST /acks {vqibl}
    API->>DB: MarkAcked
    API-->>Dev: 204 No Content

    Note over WK,DB: Ahora sí: acked es terminal,<br/>Due() ya no la devuelve.

    Hue->>API: GET /vqibl
    API-->>Hue: 200 {reservationId: 1121231, guest, createdAt}
```

---

## 3. El dispositivo está apagado

Lo que probás con `make run-guest GUEST_FLAGS="--offline"`.

```mermaid
sequenceDiagram
    autonumber
    participant WK as Worker
    participant DB as store.json
    participant Dev as Dispositivo

    Note over Dev: apagado (--offline)

    loop cada tick, con backoff creciente
        WK->>DB: Due(now)
        WK->>Dev: POST /notifications
        Dev-->>WK: 503 device offline
        WK->>DB: RecordAttempt<br/>lastError=503, next = ahora + backoff
        Note right of WK: intento 1 → +1s<br/>intento 2 → +2s<br/>intento 3 → +4s<br/>intento 4 → +8s<br/>… tope en 30s
    end

    Note over Dev: el huésped prende el dispositivo

    WK->>Dev: POST /notifications
    Dev-->>WK: 200 OK
    Dev->>Dev: 📱 nueva confirmación
    Dev->>DB: (vía /acks) MarkAcked
    Note over WK,DB: los reintentos se detienen
```

**Sin máximo de intentos**: la consigna pide reintentar hasta entregar. El tope
de 30 segundos evita bombardear al dispositivo, y el jitter de ±20% evita que
muchas entregas vencidas salgan todas juntas.

---

## 4. El ack se pierde — la garantía central

Lo que probás con `--ack-loss-rate=0.8`. **Este es el diagrama que conviene
mostrar en la entrevista.**

```mermaid
sequenceDiagram
    autonumber
    participant WK as Worker
    participant Dev as Dispositivo
    actor Hue as Huésped

    WK->>Dev: POST /notifications {vqibl}
    Dev->>Dev: ¿"vqibl" ya lo vi?<br/>NO → lo registro
    Dev->>Hue: 📱 muestra "vqibl"
    Dev--xWK: ack perdido 📡

    Note over WK: silencio: el servidor no sabe<br/>si llegó o no. Reintenta.

    WK->>Dev: POST /notifications {vqibl}
    Dev->>Dev: ¿"vqibl" ya lo vi?<br/>SÍ → 🔁 NO lo muestro
    Dev--xWK: ack perdido 📡

    WK->>Dev: POST /notifications {vqibl}
    Dev->>Dev: SÍ → 🔁 NO lo muestro
    Dev->>WK: ✅ POST /acks {vqibl}

    Note over WK,Hue: 3 notificaciones recibidas.<br/>1 sola vez visto por el huésped.
```

El dispositivo **re-ackea los duplicados**, y eso es lo que corta el loop: si el
servidor reenvió, es porque el ack anterior no le llegó.

Verificable con `GET :4000/confirmations`:

```json
{ "confirmationCode": "vqibl", "notifications": 3, "displays": 1 }
```

---

## 5. Por qué exactly-once no se puede

El argumento que justifica todo el diseño.

```mermaid
flowchart TB
    S["El servidor envió la notificación<br/>y no recibió ack"]

    S --> A["<b>Mundo A</b><br/>La notificación nunca llegó.<br/>El huésped no tiene su código."]
    S --> B["<b>Mundo B</b><br/>Llegó y el huésped lo vio,<br/>pero el ack se perdió."]

    A --> X{"Para el servidor<br/>los dos mundos son<br/>IDÉNTICOS: silencio"}
    B --> X

    X --> N["Si NO reintenta:<br/>en el mundo A el huésped<br/>pierde su código.<br/><b>Inaceptable.</b>"]
    X --> Y["Si reintenta:<br/>en el mundo B el dispositivo<br/>recibe un duplicado."]

    Y --> SOL["<b>Elegimos reintentar:</b><br/><b>at-least-once</b><br/>El duplicado se resuelve<br/>en el dispositivo,<br/>que deduplica por código."]

    style N fill:#ffe0e0,stroke:#c00
    style SOL fill:#e0ffe0,stroke:#0a0
    style X fill:#fff4d0,stroke:#c90
```

Por eso **"nunca dos veces" significa que el huésped nunca lo *ve* dos veces**,
no que el dispositivo nunca lo *reciba* dos veces. Es la única interpretación
implementable, y es la que usan las colas reales con sus claves de idempotencia.

---

## 6. Estados de una entrega

```mermaid
stateDiagram-v2
    [*] --> pending : se crea junto al booking
    pending --> sent : primer intento, éxito o error
    sent --> sent : reintento con backoff, attempts++
    pending --> acked : ack antes del primer intento
    sent --> acked : POST /acks
    acked --> [*]

    note right of sent
        Un 200 del webhook la deja en sent.
        Solo el ack la cierra.
    end note
```

Dos cosas que no se ven en el dibujo pero importan:

- **`acked` es terminal.** `RecordAttempt` sobre una entrega ya ackeada es un
  no-op, así que un intento que estaba en vuelo cuando llegó el ack no la reabre.
- **`Due()`** devuelve las que **no** están en `acked` y cuyo `nextAttemptAt`
  ya pasó. Por eso el ack detiene los reintentos sin que nadie cancele nada.

---

## 7. Un tick del worker

```mermaid
flowchart TB
    T["tick cada 500ms"] --> DUE["Due(now, batch=100)<br/>entregas vencidas, las más viejas primero"]
    DUE --> LOOP{"para cada entrega"}

    LOOP --> CLAIM{"¿ya hay un intento<br/>en vuelo para<br/>este código?"}
    CLAIM -->|"sí"| SKIP["saltear"]
    CLAIM -->|"no"| POOL["tomar un slot del pool<br/>(máximo 4 en paralelo)"]

    POOL --> ATT["Attempt: POST al webhook<br/>timeout de 2s"]
    ATT --> REC["RecordAttempt<br/>attempts++, próximo intento con backoff"]
    REC --> REL["liberar el slot y el código"]

    SKIP --> LOOP
    REL --> LOOP

    style CLAIM fill:#fff4d0,stroke:#c90
    style POOL fill:#e8f0ff,stroke:#48c
```

Dos protecciones concretas: el **pool de 4** evita que un dispositivo lento
frene al resto, y la marca de **en vuelo** evita que dos ticks tomen la misma
entrega (un intento puede durar más que un tick).

---

## 8. Transactional outbox y reinicio

El booking y su entrega se guardan juntos o no se guardan.

```mermaid
sequenceDiagram
    autonumber
    participant SV as service
    participant ST as Store (memoria)
    participant FS as disco

    SV->>ST: CreateWithDelivery(booking, delivery)
    ST->>ST: lock

    alt el reservationId ya existe
        ST-->>SV: booking existente, created=false
        Note over ST: idempotencia: la misma reserva<br/>nunca recibe un segundo código
    else el código ya está tomado
        ST-->>SV: ErrCodeCollision
        Note over SV: el service regenera el código<br/>(hasta 10 intentos)
    else nuevo
        ST->>ST: escribe los 3 mapas
        ST->>FS: snapshot: tmp + fsync + rename
        alt la escritura falla
            ST->>ST: revierte los 3 mapas
            ST-->>SV: error
            Note over ST,FS: memoria y disco nunca divergen
        else ok
            ST-->>SV: booking, created=true
        end
    end
```

Y al reiniciar:

```mermaid
flowchart LR
    A["arranca el servidor"] --> B["lee data/store.json"]
    B --> C{"¿existe?"}
    C -->|"no"| D["arranque en frío"]
    C -->|"corrupto"| E["falla al arrancar"]
    C -->|"sí"| F["carga bookings<br/>y deliveries"]
    F --> G["el worker hace Due()<br/>y ve las no-acked"]
    G --> H["las entregas pendientes<br/>se retoman solas"]

    style E fill:#ffe0e0,stroke:#c00
    style H fill:#e0ffe0,stroke:#0a0
```

Falla al arrancar con un snapshot corrupto **a propósito**: arrancar en frío
perdería entregas pendientes en silencio.

---

## 9. Unicidad del código bajo concurrencia

```mermaid
sequenceDiagram
    autonumber
    participant R1 as Request A
    participant R2 as Request B
    participant ST as Store (un solo mutex)

    par dos requests simultáneos
        R1->>R1: genera "vqibl"
    and
        R2->>R2: genera "vqibl" (colisión)
    end

    R1->>ST: CreateWithDelivery("vqibl")
    ST-->>R1: ok, created=true

    R2->>ST: CreateWithDelivery("vqibl")
    ST-->>R2: ErrCodeCollision

    R2->>R2: regenera → "a2b34"
    R2->>ST: CreateWithDelivery("a2b34")
    ST-->>R2: ok, created=true
```

El check y el insert ocurren **bajo el mismo lock**, así que no hay ventana
entre "¿existe?" y "lo guardo". Sin eso, dos requests concurrentes podrían
llevarse el mismo código.

---

## Cómo se relaciona con el código

| Diagrama | Archivo |
|---|---|
| 1. Procesos y capas | `cmd/server/main.go`, `cmd/guest/main.go` |
| 2. Camino feliz | `internal/handler/booking_handler.go`, `internal/worker/delivery_worker.go` |
| 3. Reintentos | `internal/service/delivery_service.go` (`NextAttemptAt`) |
| 4. Dedupe | `cmd/guest/main.go` (`record`) |
| 6. Estados | `internal/model/delivery.go` |
| 7. Tick | `internal/worker/delivery_worker.go` (`tick`) |
| 8. Outbox | `internal/repository/store.go` (`CreateWithDelivery`) |
| 9. Unicidad | `internal/service/booking_service.go` (`Create`) |
