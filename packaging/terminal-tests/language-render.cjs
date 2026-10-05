'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

function dependency(name) {
  const modules = process.env.FLCLASH_TUI_XTERM_MODULES;
  return require(modules ? path.join(modules, name) : name);
}

let Terminal;
let Unicode11Addon;
try {
  ({ Terminal } = dependency('@xterm/headless'));
  ({ Unicode11Addon } = dependency('@xterm/addon-unicode11'));
} catch (error) {
  if (error.code !== 'MODULE_NOT_FOUND') {
    throw error;
  }
  console.error('Terminal test dependencies unavailable: npm ci --prefix packaging/terminal-tests');
  process.exit(78);
}

if (process.argv[2] === '--check') {
  process.exit(0);
}

function terminal(width, height) {
  const term = new Terminal({ cols: width, rows: height, allowProposedApi: true });
  term.loadAddon(new Unicode11Addon());
  term.unicode.activeVersion = '11';
  return term;
}

function write(term, value) {
  return new Promise(resolve => term.write(value, resolve));
}

function checkFrame(term, frame) {
  assert.equal(frame.lines.length, frame.height, frame.name + ': invalid expected height');
  for (let row = 0; row < frame.height; row++) {
    // This is an independent terminal-cell implementation. Checking the Go
    // renderer against its own width helper would miss spacing-mark bugs.
    const cells = term._core.unicodeService.getStringCellWidth(frame.lines[row]);
    assert.equal(cells, frame.width, `${frame.name} row ${row}: terminal cell width`);
    assert.equal(
      // After shrinking, xterm may retain off-screen cells in the line until
      // the next reflow. Compare only the visible terminal columns.
      term.buffer.active.getLine(row).translateToString(false, 0, frame.width),
      frame.lines[row],
      `${frame.name} row ${row}: screen content / border / stale-text mismatch`,
    );
    assert.equal(term.buffer.active.getLine(row).isWrapped, false, `${frame.name} row ${row}: unexpected automatic wrap`);
  }
  assert.equal(term.buffer.active.baseY, 0, frame.name + ': unexpected terminal scrolling');
}

async function main() {
  const fixtures = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
  const terminals = new Map();
  try {
    for (const frame of fixtures.frames) {
      const size = `${frame.width}x${frame.height}`;
      if (!terminals.has(size)) {
        const term = terminal(frame.width, frame.height);
        await write(term, '\x1b[?1049h');
        terminals.set(size, term);
      }
      const term = terminals.get(size);
      await write(term, '\x1b[2J\x1b[H' + frame.data);
      checkFrame(term, frame);
    }
    const stream = terminal(fixtures.stream[0].width, fixtures.stream[0].height);
    terminals.set('stream', stream);
    for (const frame of fixtures.stream) {
      if (stream.cols !== frame.width || stream.rows !== frame.height) {
        stream.resize(frame.width, frame.height);
      }
      // Feed the actual incremental output produced by Bubble Tea, not a
      // reconstruction of its repaint/cursor-positioning algorithm.
      await write(stream, frame.data);
      checkFrame(stream, frame);
    }
    console.log(`Verified ${fixtures.frames.length} full frames and ${fixtures.stream.length} Bubble Tea transitions with xterm Unicode 11.`);
  } finally {
    for (const term of terminals.values()) {
      term.dispose();
    }
  }
}

main().catch(error => {
  console.error(error.message);
  process.exitCode = 1;
});
