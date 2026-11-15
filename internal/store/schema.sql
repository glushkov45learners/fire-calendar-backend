-- Schema from contract 2. Idempotent: applied on every API start.
CREATE EXTENSION IF NOT EXISTS postgis;

CREATE TABLE IF NOT EXISTS hotspot (
    id         bigserial PRIMARY KEY,
    acq_ts     timestamptz NOT NULL,
    acq_date   date NOT NULL,
    geom       geometry(Point, 4326) NOT NULL,
    instrument text NOT NULL,      -- MODIS | VIIRS
    satellite  text NOT NULL,      -- Terra | Aqua | SNPP | NOAA20 | NOAA21
    conf       char(1) NOT NULL,   -- l | n | h
    conf_raw   text,
    frp        real,
    daynight   char(1),            -- D | N
    type       smallint,           -- NULL for NRT
    product    text NOT NULL,      -- SP | NRT
    cell_id    int NOT NULL,
    lc         smallint            -- IGBP class 1–17 (MCD12Q1 LC_Type1), NULL if unknown
);
-- Tables created before lc was added.
ALTER TABLE hotspot ADD COLUMN IF NOT EXISTS lc smallint;
CREATE INDEX IF NOT EXISTS hotspot_geom_idx ON hotspot USING gist (geom);
CREATE INDEX IF NOT EXISTS hotspot_acq_date_idx ON hotspot (acq_date);
CREATE INDEX IF NOT EXISTS hotspot_cell_date_idx ON hotspot (cell_id, acq_date);

CREATE TABLE IF NOT EXISTS grid_cell (
    cell_id int PRIMARY KEY,
    center  geometry(Point, 4326) NOT NULL,
    geom    geometry(Polygon, 4326) NOT NULL
);
CREATE INDEX IF NOT EXISTS grid_cell_center_idx ON grid_cell USING gist (center);
CREATE INDEX IF NOT EXISTS grid_cell_geom_idx ON grid_cell USING gist (geom);

CREATE TABLE IF NOT EXISTS cell_day (
    cell_id     int  NOT NULL,
    date        date NOT NULL,
    modis_count int  NOT NULL,
    viirs_count int  NOT NULL,
    count_h     real NOT NULL,
    frp_h       real NOT NULL,
    PRIMARY KEY (cell_id, date)
);
CREATE INDEX IF NOT EXISTS cell_day_date_idx ON cell_day (date);

CREATE TABLE IF NOT EXISTS model_meta (
    model_version text PRIMARY KEY,
    loaded_at     timestamptz NOT NULL DEFAULT now(),
    body          jsonb NOT NULL
);

-- Monthly digest (not part of contract 2): who asked for it, and which months
-- the scheduler has already mailed.
CREATE TABLE IF NOT EXISTS subscriber (
    email           text PRIMARY KEY,           -- lower-cased
    token           text NOT NULL UNIQUE,       -- secret of the unsubscribe link
    consent_at      timestamptz NOT NULL DEFAULT now(),
    unsubscribed_at timestamptz
);

CREATE TABLE IF NOT EXISTS digest_run (
    period     text PRIMARY KEY,                -- YYYY-MM
    started_at timestamptz NOT NULL DEFAULT now(),
    sent       int,
    failed     int
);

-- 0.1° grid over the coverage bbox 22.0,44.0,40.3,52.4 (contract 1 formula:
-- cx = floor((lon + 180) * 10), cy = floor((lat + 90) * 10), id = cy * 3600 + cx).
-- The bbox reported by /meta is the extent of this table.
INSERT INTO grid_cell (cell_id, center, geom)
SELECT cy * 3600 + cx,
       ST_SetSRID(ST_MakePoint((cx - 1800 + 0.5) / 10.0, (cy - 900 + 0.5) / 10.0), 4326),
       ST_MakeEnvelope((cx - 1800) / 10.0, (cy - 900) / 10.0,
                       (cx - 1799) / 10.0, (cy - 899) / 10.0, 4326)
FROM generate_series(2020, 2202) AS cx,
     generate_series(1340, 1423) AS cy
ON CONFLICT (cell_id) DO NOTHING;
