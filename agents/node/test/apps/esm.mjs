// ESM e2e app: `node --import <dist/esm/register.js> esm.mjs`; the agent API is imported through the ESM facade.
import express from 'express';
import { trace } from '@opentelemetry/api';
import { getAgent, SAMPLING_RATIO_KEY } from '../../dist/esm/index.js';

const app = express();
app.get('/items/:id', (req, res) => {
  const span = trace.getActiveSpan();
  res.json({ id: req.params.id, traced: Boolean(span), agent: Boolean(getAgent()), key: SAMPLING_RATIO_KEY });
});
const server = app.listen(0, '127.0.0.1', () => {
  console.log(`READY ${server.address().port}`);
});
