// Zero-code start: `node --require openlog-node/register app.js` (CommonJS applications).
// ECMAScript module applications use `node --import openlog-node/register app.mjs`, which additionally registers
// the import-in-the-middle loader hook (dist/esm/register.js) and then loads this file.
import { startFromEnvironment } from './index';

startFromEnvironment();
