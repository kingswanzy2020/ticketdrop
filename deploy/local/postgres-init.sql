-- One database per service, as in the cluster. Locally they share a server.
-- Each service creates its own tables when it starts.
CREATE DATABASE catalog;
CREATE DATABASE orders;
CREATE DATABASE inventory;
CREATE DATABASE payments;
CREATE DATABASE fulfillment;
-- The scheduler stores nothing. Its database is where its leader lock lives.
CREATE DATABASE scheduler;
