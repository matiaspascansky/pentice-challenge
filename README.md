# Booking Confirmation Service

Cuando un huésped completa una reserva, el sistema genera un **código de confirmación** de 5 caracteres alfanuméricos. El código **no viaja en la respuesta del POST**: se entrega *out-of-band* al dispositivo del huésped, que puede estar offline, así que el servidor **reintenta hasta recibir un ack**. El huésped **nunca ve la misma confirmación dos veces**.

Stack: **Go + [chi](https://github.com/go-chi/chi)**. Sin base de datos, sin colas, sin Redis.

---

## Cómo correrlo

Requisitos: Go 1.26 (no hay otra dependencia más que chi).

Hacen falta **dos terminales**, porque el dispositivo del huésped es un proceso aparte:

```bash
# Terminal 1 — el servidor de reservas (:3000)
make run-server

# Terminal 2 — el dispositivo simulado del huésped (:4000)
make run-guest
```

```bash
make test      # tests con el detector de carreras
make cover     # cobertura total
make check     # gofmt + vet + tests
make help      # todos los targets
```

---

## API

| Método | Ruta | Qué hace |
|---|---|---|
| `POST` | `/bookings` | Crea la reserva. **`202 Accepted` sin el código.** |
| `GET` | `/{code}` | Devuelve la reserva. `200`, `404` o `400` si el formato es inválido. |
| `POST` | `/acks` | El dispositivo confirma que procesó el código. `204`. Idempotente. |
| `GET` | `/bookings/{code}/delivery` | Estado de la entrega (extra, para observar los reintentos). |
| `GET` | `/health` | `200`. |

```bash
# Crear una reserva: la respuesta no trae el código
curl -X POST localhost:3000/bookings \
  -d '{"reservationId":"R123","guest":"ana@example.com"}'
# → 202 {"reservationId":"R123","status":"accepted"}

# El código aparece en la terminal del dispositivo:
#   📱 nueva confirmación confirmationCode=a2b34 reservationId=R123

# Y con el código se consulta la reserva (case-insensitive)
curl localhost:3000/a2b34
# → 200 {"confirmationCode":"a2b34","reservationId":"R123","guest":"ana@example.com","createdAt":"..."}
```

El dispositivo también expone `GET localhost:4000/confirmations`, que muestra cuántas veces recibió cada código y cuántas veces lo **mostró** (debe ser siempre 1).

---

## Demo: el dispositivo offline

Es el escenario que justifica todo el diseño.

```bash
# Terminal 1
make run-server

# Terminal 2 — el dispositivo arranca APAGADO
make run-guest GUEST_FLAGS="--offline"

# Terminal 3
curl -X POST localhost:3000/bookings \
  -d '{"reservationId":"R777","guest":"ana@example.com"}'
```

En la terminal del servidor se ven los reintentos con el backoff creciendo:

```
WARN delivery attempt failed confirmationCode=cv9vd attempt=1 error="webhook responded 503: device offline" nextAttemptAt=...
WARN delivery attempt failed confirmationCode=cv9vd attempt=2 ...
WARN delivery attempt failed confirmationCode=cv9vd attempt=3 ...
```

El estado de la entrega lo confirma:

```bash
curl localhost:3000/bookings/cv9vd/delivery
# → {"confirmationCode":"cv9vd","status":"sent","attempts":6,"nextAttemptAt":"...","lastError":"webhook responded 503: device offline"}
```

Ahora **matá el dispositivo y levantalo sano** (`Ctrl-C` en la terminal 2, después `make run-guest`). En segundos:

```
📱 nueva confirmación   confirmationCode=cv9vd reservationId=R777
✅ ack enviado          confirmationCode=cv9vd
```

Y los reintentos paran: `{"status":"acked","attempts":6,"ackedAt":"..."}`.

> **También sobrevive al reinicio del servidor.** Pará el servidor con el dispositivo apagado, volvé a levantarlo, y la entrega pendiente se retoma sola: está en el snapshot en disco.

### Demo: el ack se pierde

Acá se ve la garantía de "nunca dos veces":

```bash
make run-guest GUEST_FLAGS="--ack-loss-rate=0.8"
```

```
📱 nueva confirmación         a2b34      ← se le muestra al huésped UNA vez
📡 ack perdido en el camino   a2b34
🔁 duplicada: no se muestra   a2b34      ← el servidor reenvió
📡 ack perdido en el camino   a2b34
🔁 duplicada: no se muestra   a2b34
✅ ack enviado                a2b34      ← este llegó, y cortó los reintentos
```

```bash
curl localhost:4000/confirmations
# → [{"confirmationCode":"a2b34",...,"notifications":4,"displays":1,"acksSent":1}]
#                                        4 entregas ───┘        1 sola vez visto ─┘
```

### Flags de falla del dispositivo

| Flag | Simula |
|---|---|
| `--offline` | Dispositivo apagado: responde `503` a todo. |
| `--drop-rate=0.3` | Recibe y responde `200`, pero no procesa ni ackea. |
| `--ack-loss-rate=0.3` | Muestra el código, pero el ack se pierde en el camino. |
| `--ack-delay=200ms` | Cuánto tarda el dispositivo en ackear. |

`--drop-rate` es el más revelador: el servidor ve `200 OK` y **aun así sigue reintentando**, porque un `200` no es un ack.

---

## La garantía: at-least-once + deduplicación en el dispositivo

Es la decisión central, y conviene ser explícito.

**Exactly-once no es alcanzable solo desde el servidor.** Si el servidor envía la notificación y el ack se pierde, el servidor queda en un estado indistinguible de "la notificación nunca llegó". No tiene forma de saber cuál de las dos cosas pasó. Sus dos opciones son:

- **No reintentar** → se pierden confirmaciones cuando el ack se pierde. Inaceptable: el huésped se queda sin su código.
- **Reintentar** → el dispositivo puede recibir la misma notificación más de una vez.

Este servicio elige reintentar (**at-least-once**) y resuelve el duplicado del otro lado: **el dispositivo deduplica por `confirmationCode`**. Si ya vio ese código, **no lo muestra de nuevo** pero **sí re-ackea**, porque si el servidor reenvió es porque el ack anterior no le llegó, y re-ackear es lo que corta el loop.

O sea: "nunca dos veces" significa que el huésped nunca **ve ni procesa** la misma confirmación dos veces, aunque el dispositivo la **reciba** varias. Esa es la única interpretación implementable en un sistema distribuido, y es la que usan en la práctica las colas reales (SQS, Kafka) con sus claves de idempotencia.

Un corolario que vale la pena marcar: **un `200` del webhook no es un ack.** Significa que el request llegó, no que el dispositivo lo procesó. Por eso el servidor agenda el próximo intento incluso cuando el envío salió bien, y lo único que marca la entrega como terminal es el `POST /acks`.

---

## Arquitectura

```
Cliente ──POST /bookings──▶  API (chi)  ──▶  Repository (archivo)  ◀──  Delivery worker (ticker)
                                 ▲                                          │
                                 │                                          │ POST /notifications
                                 └────────── POST /acks ◀─── Dispositivo del huésped (proceso aparte)
```

Capas clásicas, dependencias hacia adentro:

```
handler  →  service  →  repository
               ↑
            worker  →  client (webhook)
```

- **handler**: HTTP. Parseo, validación, mapeo a status codes. Sin lógica de negocio.
- **service**: la lógica. Crear reservas, resolver códigos, intentar entregas, procesar acks.
- **repository**: persistencia detrás de interfaces, con implementaciones `file` y `memory`.
- **worker**: el loop con ticker que dispara las entregas vencidas.
- **client**: el cliente HTTP hacia el dispositivo, detrás de la interfaz `Notifier`.

```
cmd/server    el servicio
cmd/guest     el dispositivo simulado
internal/
  model       dominio y errores centinela
  config      env vars con defaults
  handler     rutas, handlers y helpers de respuesta
  service     booking_service, delivery_service, codegen
  repository  interfaces + store compartido; memory/ y file/
  worker      delivery_worker
  client      webhook_client
```

### Modelo

**`Booking` es el dato** (qué se reservó; inmutable). **`Delivery` es la tarea de avisarle al huésped** (estado operativo; cambia hasta el ack).

```
pending ──intento──▶ sent ──ack──▶ acked
                      │  ▲
                      └──┘ sin ack → reintento con backoff
```

Se guardan **en la misma operación atómica** (*transactional outbox*): si se guardara solo el booking y el proceso cayera, la entrega se perdería y el huésped nunca recibiría su código.

---

## Decisiones y supuestos

| Decisión | Elección | Por qué |
|---|---|---|
| Canal de entrega | Webhook HTTP + ack asíncrono por `POST /acks` | Separa "llegó el request" de "el dispositivo lo procesó". Es lo que permite modelar el ack perdido. |
| Garantía | At-least-once + dedupe en el dispositivo | Exactly-once no se puede solo desde el servidor (ver arriba). |
| Persistencia | Estado en memoria + snapshot JSON atómico, detrás de una interfaz | Sobrevive restarts sin meter una base de datos en un challenge de 4 horas. |
| Atomicidad booking+delivery | Un único mutex en el store | El par se guarda junto o no se guarda. |
| Scheduling | Worker con ticker que escanea las vencidas | Lo más simple que funciona a esta escala. |
| Backoff | Exponencial con jitter ±20%, base 1s, tope 30s, **sin máximo de intentos** | La consigna pide reintentar hasta entregar; el tope evita bombardear al dispositivo; el jitter evita que muchas entregas salgan en bloque. |
| Códigos | `crypto/rand` sobre `[a-z0-9]`, 5 chars (36⁵ ≈ 60M) | `crypto/rand` porque un código adivinable deja leer reservas ajenas. |
| Case-sensitivity | **Case-insensitive**, se normaliza a minúsculas | El huésped lo va a dictar por teléfono. |
| Unicidad | Check + insert atómico bajo lock; si colisiona, regenera (10 intentos) | Evita que dos requests concurrentes se lleven el mismo código. |
| Idempotencia del POST | Mismo `reservationId` → mismo código, sin segunda entrega | Si no, un reintento del cliente le mandaría al huésped dos códigos distintos para la misma reserva. |
| Respuesta del POST | `202 Accepted` sin el código | Lo pide la consigna: el código va out-of-band. |
| I/O "asíncrono" | Todas las operaciones del repositorio toman `context.Context` y devuelven `error` | Respeta la consigna de tratar lectura y escritura como asíncronas. |

### Supuestos que tomé

1. **Las entregas pendientes deben sobrevivir un reinicio del servidor.** Por eso la persistencia en archivo desde el principio.
2. **El POST repetido con el mismo `reservationId` es idempotente**, en lugar de crear una segunda reserva.
3. **Los códigos son case-insensitive.**
4. **"Nunca dos veces" se refiere a lo que el huésped ve**, no a cuántos requests recibe su dispositivo.

Si alguno de estos no coincide con lo que esperaban, los cuatro están aislados en un solo lugar del código y son fáciles de cambiar.

---

## Tests

```bash
make test     # -race, 111 casos
make cover
```

| Cubre | Dónde |
|---|---|
| Formato, alfabeto y normalización de los códigos | `internal/service/codegen_test.go` |
| 300 bookings concurrentes → 300 códigos distintos | `internal/service/booking_service_test.go` |
| Colisión de código → el service regenera | `internal/service/booking_service_test.go` |
| Idempotencia del POST: un booking y una entrega | `internal/service/booking_service_test.go` |
| Un `200` no corta los reintentos; solo el ack | `internal/service/delivery_service_test.go` |
| `acked` es terminal: un intento tardío no lo reabre | `internal/service/delivery_service_test.go` |
| Backoff dentro del ±20% y topeado, sin desbordes | `internal/service/delivery_service_test.go` |
| El contrato del repositorio, contra `memory` **y** `file` | `internal/repository/store_test.go` |
| Reinicio: las entregas pendientes se retoman | `internal/repository/store_test.go` |
| Snapshot corrupto → falla al arrancar | `internal/repository/store_test.go` |
| El worker no toma la misma entrega en paralelo | `internal/worker/delivery_worker_test.go` |
| Reintentos hasta el ack, y silencio después | `internal/worker/delivery_worker_test.go` |
| Handlers con `httptest`, incluido que el POST no filtre el código | `internal/handler/router_test.go` |
| El dispositivo muestra cada código una sola vez, con 100 notificaciones simultáneas | `cmd/guest/main_test.go` |

Cobertura: `internal/service` 96.6%, `internal/worker` 96.1%, `internal/repository` 94.2%, `internal/client` 93.8%, `internal/handler` 86.0%, `internal/config` 100%. `cmd/server` queda sin cubrir: es solo el wiring y el graceful shutdown.

---

## Configuración

| Variable | Default |
|---|---|
| `PORT` | `3000` |
| `GUEST_WEBHOOK_URL` | `http://localhost:4000/notifications` |
| `DATA_FILE` | `./data/store.json` |
| `WORKER_TICK` | `500ms` |
| `BACKOFF_BASE` | `1s` |
| `BACKOFF_MAX` | `30s` |
| `NOTIFY_TIMEOUT` | `2s` |

Una duración mal escrita hace fallar el arranque, en vez de dejar al servicio corriendo con un valor que nadie quiso.

---

## Trade-offs y qué haría en producción

Lo que está acá es deliberadamente lo más simple que cumple la consigna. En producción cambiaría:

- **Base de datos real** en lugar del snapshot JSON. El snapshot reescribe el estado completo en cada mutación: es `O(n)` por escritura y no escala más allá de unos miles de reservas. Una alternativa intermedia sin DB sería un **log append-only (WAL) + replay** al arrancar.
- **Outbox relay a una cola real** (SQS, Kafka) en lugar del ticker, con los reintentos y el backoff delegados a la cola. El worker actual escanea *todas* las entregas no-acked en cada tick.
- **DLQ y alertas** para entregas muy viejas. Hoy una entrega sin ack reintenta para siempre cada 30 segundos: nadie se entera de que un dispositivo quedó muerto.
- **Autenticación del webhook**, firmando el payload con HMAC, para que el dispositivo pueda verificar que la notificación viene de nosotros.
- **Rate limiting en `GET /{code}`**. Con 36⁵ ≈ 60M combinaciones, un atacante puede enumerar códigos y leer reservas ajenas. Es el agujero de seguridad más concreto que tiene el servicio hoy.
- **Métricas** (entregas pendientes, intentos por entrega, latencia hasta el ack) en lugar de solo logs.
- **Idempotency keys** en el POST, en lugar de usar el `reservationId` para eso.

### Limitaciones conocidas

- **El dedupe del dispositivo vive en memoria.** Si el dispositivo se reinicia, pierde el set de códigos vistos y volvería a mostrar uno ya visto si el servidor lo reenvía. En un dispositivo real eso sería almacenamiento local persistente; acá no lo hice porque el foco del challenge está en el servidor.
- **Un solo proceso servidor.** El lock del store es in-process: dos instancias sobre el mismo archivo se pisarían. Con una DB real esto se resuelve con una transacción y un índice único.
- **El snapshot se escribe tomando el lock**, así que un `fsync` lento frena las escrituras. A esta escala es imperceptible.
- **`GET /acks` devuelve 400 en vez de 405**, porque el wildcard `GET /{code}` atrapa la ruta y `acks` no cumple el formato de un código.

---

## Preguntas que le haría al equipo

1. ¿Un POST repetido con el mismo `reservationId` debe ser idempotente, o cada llamada es una reserva nueva?
2. ¿Hace falta un máximo de intentos y una DLQ, o reintentar indefinidamente es el comportamiento deseado?
