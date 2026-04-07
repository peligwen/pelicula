/* qr.js — minimal QR code SVG generator
 * Byte mode, EC level M, versions 1–6.
 * Exports: window.qrSVG(text, moduleSize=6) → SVG string, or '' if text is too long.
 */
(function (global) {
  'use strict';

  // ── GF(256) arithmetic ────────────────────────────────────────────────────
  const EXP = new Uint8Array(512);
  const LOG = new Uint8Array(256);
  (function () {
    let x = 1;
    for (let i = 0; i < 255; i++) {
      EXP[i] = x;
      LOG[x] = i;
      x = x & 128 ? (x << 1) ^ 0x11d : x << 1;
    }
    for (let i = 255; i < 512; i++) EXP[i] = EXP[i - 255];
  })();

  function gfMul(a, b) {
    return a && b ? EXP[LOG[a] + LOG[b]] : 0;
  }

  // Generator polynomial for n EC codewords: g(x) = ∏(x + α^i), i=0..n-1
  function rsPoly(n) {
    let p = [1];
    for (let i = 0; i < n; i++) {
      const r = new Array(p.length + 1).fill(0);
      for (let a = 0; a < p.length; a++) {
        r[a]     ^= p[a];
        r[a + 1] ^= gfMul(p[a], EXP[i]);
      }
      p = r;
    }
    return p;
  }

  // Compute n EC codewords for data[] using polynomial long division.
  function rsEncode(data, n) {
    const gen = rsPoly(n);
    const msg = [...data, ...new Array(n).fill(0)];
    for (let i = 0; i < data.length; i++) {
      const c = msg[i];
      if (c) for (let j = 0; j < gen.length; j++) msg[i + j] ^= gfMul(c, gen[j]);
    }
    return msg.slice(data.length);
  }

  // ── Version / EC parameters for EC level M ────────────────────────────────
  // [dataCapacity, ecPerBlock, group1Blocks, group1Data, group2Blocks, group2Data]
  // remainder bits for v1-v6: [0, 7, 7, 7, 7, 7]
  const VER_M = [
    null,
    [16,  10, 1, 16, 0,  0],  // v1
    [28,  16, 1, 28, 0,  0],  // v2
    [44,  26, 1, 44, 0,  0],  // v3
    [64,  18, 2, 32, 0,  0],  // v4
    [86,  24, 2, 43, 0,  0],  // v5
    [108, 16, 4, 27, 0,  0],  // v6
  ];

  // Alignment pattern centres (versions 2-6 each have one centre in the lower-right)
  const AP = [null, [], [18,18], [22,22], [26,26], [30,30], [34,34]];

  // ── Data encoding ─────────────────────────────────────────────────────────
  function encodeData(bytes, version) {
    const [dataCap] = VER_M[version];
    const bits = [];
    function pushBits(val, n) {
      for (let i = n - 1; i >= 0; i--) bits.push((val >> i) & 1);
    }
    // Mode indicator: byte mode = 0100
    pushBits(0b0100, 4);
    // Character count (8 bits for versions 1-9)
    pushBits(bytes.length, 8);
    // Data bytes
    for (const b of bytes) pushBits(b, 8);
    // Terminator
    for (let i = 0; i < 4 && bits.length < dataCap * 8; i++) bits.push(0);
    // Bit padding to byte boundary
    while (bits.length % 8) bits.push(0);
    // Byte padding
    const pads = [0xec, 0x11];
    let pi = 0;
    while (bits.length < dataCap * 8) pushBits(pads[pi++ & 1], 8);
    // Convert to codeword array
    const cw = [];
    for (let i = 0; i < bits.length; i += 8) {
      let b = 0;
      for (let j = 0; j < 8; j++) b = (b << 1) | (bits[i + j] || 0);
      cw.push(b);
    }
    return cw;
  }

  // Build interleaved codeword sequence (data then EC).
  function buildCodewords(dataCw, version) {
    const [, ecPerBlock, g1n, g1d, g2n, g2d] = VER_M[version];
    // Split into blocks
    const blocks = [];
    let pos = 0;
    for (let i = 0; i < g1n; i++) { blocks.push(dataCw.slice(pos, pos + g1d)); pos += g1d; }
    for (let i = 0; i < g2n; i++) { blocks.push(dataCw.slice(pos, pos + g2d)); pos += g2d; }
    const ecBlocks = blocks.map(b => rsEncode(b, ecPerBlock));
    // Interleave data
    const result = [];
    const maxData = Math.max(g1d, g2d);
    for (let i = 0; i < maxData; i++)
      for (const b of blocks) if (i < b.length) result.push(b[i]);
    // Interleave EC
    for (let i = 0; i < ecPerBlock; i++)
      for (const ec of ecBlocks) result.push(ec[i]);
    return result;
  }

  // ── Matrix construction ───────────────────────────────────────────────────
  function makeMatrix(size) {
    return Array.from({ length: size }, () => new Int8Array(size).fill(-1));
    // -1 = unset, 0 = light, 1 = dark
  }

  function set(M, r, c, v) { if (r >= 0 && r < M.length && c >= 0 && c < M.length) M[r][c] = v; }

  function placeFinder(M, tr, tc) {
    for (let r = 0; r < 7; r++)
      for (let c = 0; c < 7; c++) {
        const onBorder = r === 0 || r === 6 || c === 0 || c === 6;
        const inCenter = r >= 2 && r <= 4 && c >= 2 && c <= 4;
        set(M, tr + r, tc + c, onBorder || inCenter ? 1 : 0);
      }
  }

  function placeAlignment(M, cr, cc) {
    for (let r = -2; r <= 2; r++)
      for (let c = -2; c <= 2; c++) {
        const onBorder = Math.abs(r) === 2 || Math.abs(c) === 2;
        set(M, cr + r, cc + c, onBorder || (r === 0 && c === 0) ? 1 : 0);
      }
  }

  function reserveFormatInfo(M, size) {
    // Mark format info cells as reserved (value 0) around top-left finder
    const cells = formatCells(size);
    for (const [r, c] of cells[0]) if (M[r][c] < 0) M[r][c] = 0;
    for (const [r, c] of cells[1]) if (M[r][c] < 0) M[r][c] = 0;
    // Dark module
    M[size - 8][8] = 1;
  }

  function formatCells(size) {
    return [
      // Copy 1: around top-left finder
      [[8,0],[8,1],[8,2],[8,3],[8,4],[8,5],[8,7],[8,8],
       [7,8],[5,8],[4,8],[3,8],[2,8],[1,8],[0,8]],
      // Copy 2: top-right + bottom-left
      [[size-1,8],[size-2,8],[size-3,8],[size-4,8],[size-5,8],[size-6,8],[size-7,8],
       [8,size-8],[8,size-7],[8,size-6],[8,size-5],[8,size-4],[8,size-3],[8,size-2],[8,size-1]],
    ];
  }

  // Format info word for EC level M (bits 00) + mask id.
  // BCH(15,5) with generator 0x537, XOR mask 0x5412.
  function formatWord(maskId) {
    let d = maskId << 10; // EC level M = 00, so fmt data = maskId (3 bits)
    for (let i = 14; i >= 10; i--) {
      if (d & (1 << i)) d ^= 0x537 << (i - 10);
    }
    return (maskId << 10 | (d & 0x3ff)) ^ 0x5412;
  }

  function writeFormatInfo(M, size, maskId) {
    const word = formatWord(maskId);
    const cells = formatCells(size);
    // Bit 14 = MSB at index 0
    for (let i = 0; i < 15; i++) {
      const bit = (word >> (14 - i)) & 1;
      const [r1, c1] = cells[0][i];
      const [r2, c2] = cells[1][i];
      M[r1][c1] = bit;
      M[r2][c2] = bit;
    }
    // Re-place dark module (format copy 2 might overwrite it)
    M[size - 8][8] = 1;
  }

  function buildFunctionMask(M) {
    const size = M.length;
    const fm = Array.from({ length: size }, (_, r) => Array.from({ length: size }, (_, c) => M[r][c] >= 0));
    return fm;
  }

  // Place codeword bits in the matrix using the QR zigzag pattern.
  function placeData(M, codewords, version) {
    const size = M.length;
    const fm = buildFunctionMask(M);
    const bits = [];
    for (const cw of codewords) for (let i = 7; i >= 0; i--) bits.push((cw >> i) & 1);
    // Remainder bits (0-fill)
    const rem = [0, 0, 7, 7, 7, 7, 7][version];
    for (let i = 0; i < rem; i++) bits.push(0);

    let bi = 0;
    let up = true;
    // Columns iterate right-to-left in pairs, skipping column 6 (timing)
    for (let col = size - 1; col >= 1; col -= 2) {
      if (col === 6) col--; // skip timing column
      for (let rr = 0; rr < size; rr++) {
        const r = up ? size - 1 - rr : rr;
        for (let dc = 0; dc <= 1; dc++) {
          const c = col - dc;
          if (!fm[r][c]) {
            M[r][c] = bi < bits.length ? bits[bi++] : 0;
          }
        }
      }
      up = !up;
    }
  }

  // Apply mask formula to data modules.
  function applyMask(M, fm, maskId) {
    const size = M.length;
    const masks = [
      (r, c) => (r + c) % 2 === 0,
      (r)    => r % 2 === 0,
      (r, c) => c % 3 === 0,
      (r, c) => (r + c) % 3 === 0,
      (r, c) => (Math.floor(r / 2) + Math.floor(c / 3)) % 2 === 0,
      (r, c) => (r * c) % 2 + (r * c) % 3 === 0,
      (r, c) => ((r * c) % 2 + (r * c) % 3) % 2 === 0,
      (r, c) => ((r + c) % 2 + (r * c) % 3) % 2 === 0,
    ];
    const fn = masks[maskId];
    for (let r = 0; r < size; r++)
      for (let c = 0; c < size; c++)
        if (!fm[r][c] && fn(r, c)) M[r][c] ^= 1;
  }

  // Compute QR penalty score for mask selection.
  function penalty(M) {
    const size = M.length;
    let score = 0;
    // Rule 1: runs of 5+ same colour in row/col
    for (let r = 0; r < size; r++) {
      for (let axis = 0; axis < 2; axis++) {
        let run = 1;
        for (let i = 1; i < size; i++) {
          const a = axis ? M[r][i - 1] : M[i - 1][r];
          const b = axis ? M[r][i]     : M[i][r];
          if (a === b) { run++; if (run === 5) score += 3; else if (run > 5) score++; }
          else run = 1;
        }
      }
    }
    // Rule 2: 2×2 same colour blocks
    for (let r = 0; r < size - 1; r++)
      for (let c = 0; c < size - 1; c++)
        if (M[r][c] === M[r][c+1] && M[r][c] === M[r+1][c] && M[r][c] === M[r+1][c+1])
          score += 3;
    // Rule 3: finder-like patterns (simplified: score is minor so skip for brevity)
    // Rule 4: dark module ratio
    let dark = 0;
    for (let r = 0; r < size; r++) for (let c = 0; c < size; c++) dark += M[r][c];
    const pct = (dark / (size * size)) * 100;
    const prev = Math.abs(Math.floor(pct / 5) * 5 - 50) / 5;
    const next = Math.abs(Math.ceil(pct / 5) * 5 - 50) / 5;
    score += Math.min(prev, next) * 10;
    return score;
  }

  // ── Public API ────────────────────────────────────────────────────────────
  function qrSVG(text, moduleSize) {
    moduleSize = moduleSize || 5;
    const bytes = new TextEncoder().encode(text);
    const len = bytes.length;

    // Pick version
    let version = 0;
    for (let v = 1; v <= 6; v++) {
      if (len <= VER_M[v][0]) { version = v; break; }
    }
    if (!version) return ''; // text too long for v6 byte/M

    const size = 4 * version + 17;

    // Encode and build codeword sequence
    const dataCw = encodeData(bytes, version);
    const allCw  = buildCodewords(dataCw, version);

    // Build matrix template (function patterns only)
    const tmpl = makeMatrix(size);
    // Finder patterns
    placeFinder(tmpl, 0, 0);
    placeFinder(tmpl, 0, size - 7);
    placeFinder(tmpl, size - 7, 0);
    // Separators (light border around finders)
    for (let i = 0; i < 8; i++) {
      set(tmpl, 7, i, 0); set(tmpl, i, 7, 0);
      set(tmpl, 7, size - 1 - i, 0); set(tmpl, i, size - 8, 0);
      set(tmpl, size - 8, i, 0); set(tmpl, size - 1 - i, 7, 0);
    }
    // Timing patterns
    for (let i = 8; i < size - 8; i++) {
      tmpl[6][i] = i % 2 === 0 ? 1 : 0;
      tmpl[i][6] = i % 2 === 0 ? 1 : 0;
    }
    // Alignment pattern
    if (AP[version].length) {
      const [ar, ac] = AP[version];
      placeAlignment(tmpl, ar, ac);
    }
    // Reserve format info cells
    reserveFormatInfo(tmpl, size);

    // Build function-module mask (true = reserved, not data)
    const fm = buildFunctionMask(tmpl);

    // Try all 8 masks, pick best
    let bestMask = 0, bestScore = Infinity;
    for (let m = 0; m < 8; m++) {
      // Deep copy template
      const M = tmpl.map(row => Int8Array.from(row));
      placeData(M, allCw, version);
      applyMask(M, fm, m);
      writeFormatInfo(M, size, m);
      const s = penalty(M);
      if (s < bestScore) { bestScore = s; bestMask = m; }
    }

    // Build final matrix with best mask
    const M = tmpl.map(row => Int8Array.from(row));
    placeData(M, allCw, version);
    applyMask(M, fm, bestMask);
    writeFormatInfo(M, size, bestMask);

    // Render SVG (4-module quiet zone)
    const quiet = 4;
    const total = (size + quiet * 2) * moduleSize;
    const rects = [];
    for (let r = 0; r < size; r++) {
      for (let c = 0; c < size; c++) {
        if (M[r][c] === 1) {
          const x = (c + quiet) * moduleSize;
          const y = (r + quiet) * moduleSize;
          rects.push(`<rect x="${x}" y="${y}" width="${moduleSize}" height="${moduleSize}"/>`);
        }
      }
    }
    return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${total} ${total}" width="${total}" height="${total}">` +
      `<rect width="100%" height="100%" fill="#fff"/>` +
      `<g fill="#000">${rects.join('')}</g>` +
      `</svg>`;
  }

  global.qrSVG = qrSVG;
})(window);
