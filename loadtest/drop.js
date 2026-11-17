// A crowd at a drop, as k6 sees it.
//
// Each virtual user is one customer with the shopfront open in a browser. They
// arrive before the opening time and wait on the drop's page, which asks every
// second whether the drop has opened, as the real page does. When it opens,
// each one takes a moment to fill in the form, buys once, and then watches
// their order until it ends, asking once a second as the real page does.
//
// Everything goes through the shopfront (web), the way a browser's requests
// do: web asks the gateway, which asks the services. The requests carry the
// HX-Request header, so web answers with the same fragments htmx would get.
//
// This script measures. Whether anything was oversold is checked afterwards in
// the databases, by scripts/demo-drop.sh, which also creates the drop and
// starts this script.
//
// Settings, as environment variables:
//   BASE       the shopfront's address
//   DROP, TIER the drop to buy, which must already be announced
//   CUSTOMERS  how many customers (default 1000)
//   ARRIVE     seconds over which they arrive, before the opening (default 15)
//   MAX_QTY    each buys 1 to MAX_QTY tickets (default 4)

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';

const BASE = __ENV.BASE;
const DROP = __ENV.DROP;
const TIER = __ENV.TIER || 'general';
const CUSTOMERS = parseInt(__ENV.CUSTOMERS || '1000', 10);
const ARRIVE = parseFloat(__ENV.ARRIVE || '15');
const MAX_QTY = parseInt(__ENV.MAX_QTY || '4', 10);

// How a buy ended. Each customer adds 1 to exactly one of the first four.
const accepted = new Counter('buy_accepted');
const soldOut = new Counter('buy_sold_out');
const refused = new Counter('buy_refused_other'); // an answer, but neither of the above
const unanswered = new Counter('buy_unanswered'); // an error or no answer at all
const neverOpened = new Counter('drop_never_opened');

const ticketed = new Counter('order_ticketed');
const failed = new Counter('order_failed');
const stillPending = new Counter('order_still_pending');

// Milliseconds the customer waits for the answer to "Buy tickets".
const buyTime = new Trend('buy_time', true);
// Milliseconds from that answer until the order has ended, as the page shows it.
const settleTime = new Trend('settle_time', true);

export const options = {
  scenarios: {
    crowd: {
      executor: 'per-vu-iterations',
      vus: CUSTOMERS,
      iterations: 1,
      maxDuration: '10m',
    },
  },
  thresholds: {
    // Every customer got an answer to "Buy tickets": accepted or sold out.
    buy_unanswered: ['count==0'],
    buy_refused_other: ['count==0'],
    drop_never_opened: ['count==0'],
    // Every accepted order ended while its customer was watching.
    order_still_pending: ['count==0'],
  },
  summaryTrendStats: ['min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

const htmx = { headers: { 'HX-Request': 'true' }, timeout: '30s' };

export default function () {
  // Customers do not all turn up at the same instant.
  sleep(Math.random() * ARRIVE);

  // Waiting on the drop's page until it opens.
  const waitUntil = Date.now() + 10 * 60 * 1000;
  let open = false;
  while (Date.now() < waitUntil) {
    const r = http.get(`${BASE}/drops/${DROP}/sale`, Object.assign({ tags: { name: 'wait for opening' } }, htmx));
    if (r.status === 200 && r.body.includes('<form')) {
      open = true;
      break;
    }
    sleep(1);
  }
  if (!open) {
    neverOpened.add(1);
    return;
  }

  // Choosing a ticket and typing an email address.
  sleep(0.5 + Math.random() * 2.5);

  const quantity = 1 + Math.floor(Math.random() * MAX_QTY);
  const started = Date.now();
  const buy = http.post(
    `${BASE}/drops/${DROP}/orders`,
    { tier: TIER, quantity: String(quantity), email: `customer${__VU}@example.com` },
    Object.assign({ tags: { name: 'buy' } }, htmx),
  );
  buyTime.add(Date.now() - started);

  const body = buy.body || '';
  const order = body.match(/\/orders\/([0-9a-f-]{36})\/status/);
  if (buy.status === 200 && order) {
    accepted.add(1);
  } else if (buy.status === 200 && body.includes('Sold out')) {
    soldOut.add(1);
    return;
  } else if (buy.status === 200) {
    refused.add(1);
    return;
  } else {
    unanswered.add(1);
    return;
  }
  check(buy, { 'buy answered with an order': () => true });

  // Watching the order until it ends.
  const answered = Date.now();
  const watchUntil = answered + 2 * 60 * 1000;
  while (Date.now() < watchUntil) {
    sleep(1);
    const r = http.get(`${BASE}/orders/${order[1]}/status`, Object.assign({ tags: { name: 'watch order' } }, htmx));
    if (r.status !== 200 || r.body.includes('every 1s')) {
      continue; // still pending, or a poll that failed: the page would ask again
    }
    settleTime.add(Date.now() - answered);
    if (r.body.includes("You're in")) {
      ticketed.add(1);
    } else {
      failed.add(1);
    }
    return;
  }
  stillPending.add(1);
}
