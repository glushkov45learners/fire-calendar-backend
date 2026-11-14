# fire-calendar-backend

Схема PostGIS і HTTP API Fire Calendar (контракти 2 і 3, NASA Space Apps 2026).

```
cmd/api            точка входу, HTTP-сервер
internal/store     schema.sql (застосовується при кожному старті) і всі SQL-запити
internal/analysis  календар, норми, аномалії — чистий Go, покрито тестами
internal/api       ендпоінти /api/v1/*, помилки, CSV
```

## Запуск

```sh
docker compose up --build          # PostGIS + API на :8080
# або локально:
DATABASE_URL=postgres://fire:fire@localhost:5432/fire?sslmode=disable go run ./cmd/api
go test ./...
```

Змінні: `DATABASE_URL` (обов'язкова), `ADDR` (типово `:8080`).
`CORS_ORIGINS` — дозволені origin через кому або `*`; порожня — CORS вимкнено (у проді фронт і API на одному origin). `GET /healthz` — перевірка БД.

## Що робить бекенд

- При старті створює таблиці з контракту 2 (`IF NOT EXISTS`) і заповнює `grid_cell` сіткою 0,1° на bbox покриття. `hotspot`, `cell_day`, `model_meta` наповнює loader.
- `GET /api/v1/meta`, `POST /api/v1/analyze`, `POST /api/v1/hotspots.csv`, `POST /api/v1/calendar.csv` — як у контракті 3.
- SQL лише підсумовує `cell_day` по клітинках області за кожен день; місяці, норма, z-score, перцентилі й класи рахуються в `internal/analysis`.

## Доповнення до контракту 3: `GET /api/v1/grid`

Шар клітинок для карти (порада ментора: показувати клітинки, а не сирі точки).

```
GET /api/v1/grid?from=2020-04-01&to=2020-04-30&record=harmonized
→ [{ "cell_id": 4934176, "count": 812.4 }, ...]
```

- `from`, `to` — включні, `YYYY-MM-DD`; `record` — `harmonized` (типово, `count_h`), `modis` або `viirs` (сирі `modis_count`, `viirs_count`).
- Відповідь — сума по кожній клітинці 0,1° за період для всього покриття, відсортована за `cell_id`. Клітинки без детекцій пропущено; порожній період — `[]`.
- Помилки: `400 invalid_request` (невідомий `record`), `400 invalid_range` (дати).

## Уточнення до контракту 3

- **Аномалії (тимчасово, до формули Артема)** — у `internal/analysis/stats.go`, функція `classify`: `pct` — перцентиль-ранг (mid-rank) серед того самого місяця базових років; `≥ 95` → `extreme`, `≥ 80` → `high`, `≤ 10` → `low`, інакше `normal`. `z = (x − mean) / sd` (вибіркове sd).
- `z`, `pct`, `current_month.ratio` можуть бути `null`: `z` — коли в базових роках немає розкиду, `ratio` — коли медіана 0.
- `current_month` порівнюється з тими самими першими `days_observed` днями місяця в базових роках, а не з повними місяцями.
- Діапазон даних (`date_min..date_max`) — ширший з `hotspot` і `cell_day`; базові роки — усі повні календарні роки в ньому. `from/to`, що частково виходять за дані, обрізаються; повністю поза даними — `invalid_range`.
- `totals.hotspots_raw` — сума `modis_count + viirs_count` з `cell_day` (без NOAA-20/21 і промислових `type`).
- Додаткові коди помилок: `400 invalid_request` (битий JSON, невідома `granularity`), `503 no_data` (база порожня).
