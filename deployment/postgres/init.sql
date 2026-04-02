-- Init script: create all service databases.
-- Runs once on first container start (docker-entrypoint-initdb.d).
-- "identity" is already created by POSTGRES_DB env var.

-- Extensions (must run in each database that needs them)
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

SELECT 'CREATE DATABASE strategist'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'strategist')\gexec

SELECT 'CREATE DATABASE investment'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'investment')\gexec

SELECT 'CREATE DATABASE orchestrator'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'orchestrator')\gexec

GRANT ALL PRIVILEGES ON DATABASE identity     TO mallow;
GRANT ALL PRIVILEGES ON DATABASE strategist   TO mallow;
GRANT ALL PRIVILEGES ON DATABASE investment   TO mallow;
GRANT ALL PRIVILEGES ON DATABASE orchestrator TO mallow;

-- Enable extensions in each database
\connect investment
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

\connect strategist
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

\connect orchestrator
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
