-- Bootstrap all service databases, then apply each service schema from
-- deployment/postgres/*_schema.sql. Keep table definitions out of this file.
-- Docker entrypoint runs this once on first postgres volume initialization.

-- "identity" is created by POSTGRES_DB.
SELECT 'CREATE DATABASE strategist'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'strategist')\gexec

SELECT 'CREATE DATABASE investment'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'investment')\gexec

SELECT 'CREATE DATABASE helm'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'helm')\gexec

SELECT 'CREATE DATABASE herald'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'herald')\gexec

GRANT ALL PRIVILEGES ON DATABASE identity     TO mallow;
GRANT ALL PRIVILEGES ON DATABASE strategist   TO mallow;
GRANT ALL PRIVILEGES ON DATABASE investment   TO mallow;
GRANT ALL PRIVILEGES ON DATABASE helm         TO mallow;
GRANT ALL PRIVILEGES ON DATABASE herald       TO mallow;

\connect identity
GRANT ALL ON SCHEMA public TO mallow;
\i /schemas/identity_schema.sql

\connect investment
GRANT ALL ON SCHEMA public TO mallow;
\i /schemas/investment_schema.sql

\connect helm
GRANT ALL ON SCHEMA public TO mallow;
\i /schemas/helm_schema.sql

\connect strategist
GRANT ALL ON SCHEMA public TO mallow;
\i /schemas/strategist_schema.sql

\connect herald
GRANT ALL ON SCHEMA public TO mallow;
\i /schemas/herald_schema.sql
