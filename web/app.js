'use strict';

const $ = (id) => document.getElementById(id);

const state = {
  cfg: null,
  user: null,         // {email} when signed in
  afterLogin: null,   // what to carry on with once the sign-in sheet succeeds
  loginMode: 'signin', // which form the sign-in sheet shows; see LOGIN_MODES
  resetToken: null,   // from an emailed reset link (?reset=…)
  pickup: null,       // {lat, lng, label?}
  dest: null,
  stop: null,         // optional point between pickup and dest
  stopOn: false,      // stop row shown (set or waiting for a tap)
  waitingMin: 0,
  active: 'pickup',
  multiplier: 1,
  estimate: null,
  places: [],
  markers: { pickup: null, stop: null, dest: null },
  editingTripId: null,
  viewingTrip: null,
  routeCoords: null,
  cardCollapsed: false,
  saving: false,
  savedKey: null,     // estimate fingerprint after a successful save
  rec: null,          // trip being recorded: {startedAt, points, quotedFare, multiplier, …}
  measured: null,     // last recorded trip once priced: {startedAt, endedAt, points, track, quotedFare}
  followUntil: 0,     // don't recentre on the driver until then (they panned the map)
  revSeq: { pickup: 0, stop: 0, dest: 0 },
};

const PIN_KINDS = ['pickup', 'stop', 'dest'];
const PIN_COLORS = { pickup: '#34c07c', stop: '#f0a030', dest: '#e4574f' };
const PIN_PLACEHOLDERS = {
  pickup: 'Tap the map to set pickup',
  stop: 'Tap the map to set the stop',
  dest: '…then the destination',
};

const CARD_COLLAPSE_KEY = 'menged_card_collapsed';

async function api(path, opts = {}) {
  opts.headers = Object.assign({}, opts.headers);
  if (opts.body && !opts.headers['Content-Type']) {
    opts.headers['Content-Type'] = 'application/json';
  }
  const resp = await fetch(path, opts);
  // Some answers have no body (204, and 202 from forgot-password).
  const text = await resp.text();
  let body = null;
  try { body = text ? JSON.parse(text) : null; } catch (_) { /* not JSON: an nginx error page */ }
  if (!resp.ok) {
    if (resp.status === 401 && path !== '/api/login') setUser(null);
    const err = new Error((body && body.error) || resp.statusText || 'Request failed');
    err.status = resp.status;
    throw err;
  }
  return body;
}

let map;

async function init() {
  state.cfg = await api('/api/config');
  try {
    setUser(await api('/api/me'));
  } catch (_) { /* signed out: quotes still work */ }
  openResetFromLink();
  $('save-currency').textContent = state.cfg.rates.currency;

  const c = state.cfg.map_center;
  map = new maplibregl.Map({
    container: 'map',
    style: state.cfg.map_style,
    center: [c.lng, c.lat],
    zoom: state.cfg.map_zoom || 12,
    maxBounds: state.cfg.map_max_bounds || undefined,
    minZoom: 10,
    attributionControl: { compact: true },
  });

  map.on('load', () => {
    map.addSource('route', { type: 'geojson', data: emptyLine() });
    map.addLayer({
      id: 'route-casing',
      type: 'line',
      source: 'route',
      layout: { 'line-join': 'round', 'line-cap': 'round' },
      paint: { 'line-color': '#0b1220', 'line-width': 8, 'line-opacity': 0.55 },
    });
    map.addLayer({
      id: 'route',
      type: 'line',
      source: 'route',
      layout: { 'line-join': 'round', 'line-cap': 'round' },
      paint: { 'line-color': '#5eb0ff', 'line-width': 5, 'line-opacity': 0.95 },
    });
    map.addSource('accuracy', {
      type: 'geojson',
      data: { type: 'FeatureCollection', features: [] },
    });
    map.addLayer({
      id: 'accuracy-fill',
      type: 'fill',
      source: 'accuracy',
      paint: { 'fill-color': '#4da3ff', 'fill-opacity': 0.15 },
    });
    map.addLayer({
      id: 'accuracy-outline',
      type: 'line',
      source: 'accuracy',
      paint: { 'line-color': '#4da3ff', 'line-width': 1.5, 'line-opacity': 0.7 },
    });
    map.addSource('track', { type: 'geojson', data: emptyLine() });
    map.addLayer({
      id: 'track',
      type: 'line',
      source: 'track',
      layout: { 'line-join': 'round', 'line-cap': 'round' },
      paint: { 'line-color': '#f0a030', 'line-width': 4, 'line-opacity': 0.9 },
    });
    if (pendingGeometry) drawRoute(pendingGeometry);
    if (state.rec) drawTrack();
  });

  map.on('dragstart', () => { state.followUntil = Date.now() + 15000; });

  map.on('click', (e) => {
    if (state.rec) return;
    closePlaceSearch();
    setPoint(nextPin(), { lat: e.lngLat.lat, lng: e.lngLat.lng });
  });

  if (state.cfg.geocode) {
    $('search-wrap').hidden = false;
    setupPlaceSearch();
  }

  setCardCollapsed(sessionStorage.getItem(CARD_COLLAPSE_KEY) === '1', false);
  updateCardOffset();
  requestAnimationFrame(updateCardOffset);

  await loadPlaces();
  resumeRecording();
}

function mapPadding() {
  const cardH = $('card').offsetHeight || 200;
  let top = 120, bottom = cardH + 24, left = 40, right = 40;
  const h = (map && map.getContainer().clientHeight) || 0;
  const w = (map && map.getContainer().clientWidth) || 0;
  if (h > 0 && top + bottom >= h) {
    const max = Math.max(0, h - 16);
    const scale = max / (top + bottom);
    top = Math.floor(top * scale);
    bottom = Math.floor(bottom * scale);
  }
  if (w > 0 && left + right >= w) {
    const max = Math.max(0, w - 16);
    const scale = max / (left + right);
    left = Math.floor(left * scale);
    right = Math.floor(right * scale);
  }
  return { top, bottom, left, right };
}

function updateCardOffset() {
  const h = $('card').offsetHeight || 80;
  document.documentElement.style.setProperty('--card-offset', h + 'px');
}

function setCardCollapsed(collapsed, persist) {
  state.cardCollapsed = !!collapsed;
  $('card').classList.toggle('collapsed', state.cardCollapsed);
  $('card-peek').hidden = !state.cardCollapsed;
  if (persist !== false) {
    sessionStorage.setItem(CARD_COLLAPSE_KEY, state.cardCollapsed ? '1' : '0');
  }
  updatePeek();
  requestAnimationFrame(updateCardOffset);
}

function toggleCardCollapsed() {
  setCardCollapsed(!state.cardCollapsed);
}

function updatePeek() {
  const est = state.estimate;
  if (est) {
    $('peek-stats').textContent =
      est.distance_km.toFixed(1) + ' km · ' + Math.round(est.duration_min) + ' min';
    $('peek-fare').textContent = state.cfg.rates.currency + ' ' + est.fare;
  } else if (state.pickup || state.dest) {
    $('peek-stats').textContent = 'Set both pins for a quote';
    $('peek-fare').textContent = '';
  } else {
    $('peek-stats').textContent = 'Set pickup and destination';
    $('peek-fare').textContent = '';
  }
}

function fitRouteBounds(coords) {
  coords = finiteCoords(coords);
  if (!coords.length || !map) return;
  try {
    const bounds = coords.reduce(
      (b, c) => b.extend(c),
      new maplibregl.LngLatBounds(coords[0], coords[0])
    );
    map.fitBounds(bounds, { padding: mapPadding(), maxZoom: 16 });
  } catch (err) {
    console.warn('fitBounds failed', err);
  }
}

function finiteCoords(coords) {
  if (!coords || !coords.length) return [];
  return coords.filter((c) =>
    c && c.length >= 2 && Number.isFinite(c[0]) && Number.isFinite(c[1])
  );
}

function recenterMap() {
  if (state.routeCoords && state.routeCoords.length) {
    fitRouteBounds(state.routeCoords);
    return;
  }
  if (state.pickup && state.dest) {
    fitRouteBounds(
      [state.pickup, state.stop, state.dest].filter(Boolean).map((p) => [p.lng, p.lat])
    );
    return;
  }
  if (state.pickup) {
    map.flyTo({ center: [state.pickup.lng, state.pickup.lat], zoom: 15 });
    return;
  }
  const c = state.cfg.map_center;
  map.flyTo({ center: [c.lng, c.lat], zoom: state.cfg.map_zoom || 12 });
}

function emptyLine() {
  return { type: 'Feature', geometry: { type: 'LineString', coordinates: [] } };
}

function nextPin() {
  if (!state.pickup) return 'pickup';
  if (!state.dest) return 'dest';
  if (state.stopOn && !state.stop) return 'stop';
  return state.active;
}

function setPoint(kind, pt, opts) {
  opts = opts || {};
  state.measured = null; // moving a pin turns a priced recording back into a quote
  state[kind] = {
    lat: pt.lat,
    lng: pt.lng,
    label: pt.label || undefined,
  };
  if (kind === 'stop') state.stopOn = true;
  else state.active = kind === 'pickup' ? 'dest' : 'pickup';
  state.revSeq[kind] = (state.revSeq[kind] || 0) + 1;
  const seq = state.revSeq[kind];

  if (!state.markers[kind]) {
    const m = new maplibregl.Marker({
      draggable: true,
      color: PIN_COLORS[kind],
    }).setLngLat([pt.lng, pt.lat]).addTo(map);
    m.on('dragend', () => {
      const ll = m.getLngLat();
      state.revSeq[kind] = (state.revSeq[kind] || 0) + 1;
      const dragSeq = state.revSeq[kind];
      state[kind] = { lat: ll.lat, lng: ll.lng };
      setMarkerLabel(kind, null);
      renderPoints();
      reverseLabel(kind, dragSeq);
      requestEstimate();
    });
    state.markers[kind] = m;
  } else {
    state.markers[kind].setLngLat([pt.lng, pt.lat]);
  }
  setMarkerLabel(kind, state[kind].label);
  renderPoints();
  if (!state[kind].label) reverseLabel(kind, seq);
  if (!opts.skipEstimate) requestEstimate();
}

async function reverseLabel(kind, seq) {
  if (!state.cfg || !state.cfg.geocode) return;
  const pt = state[kind];
  if (!pt || pt.label) return;
  if (seq == null) seq = state.revSeq[kind];

  const row = $('label-' + kind);
  if (row) {
    row.textContent = 'Looking up place…';
    row.classList.add('set', 'looking-up');
  }

  try {
    const r = await api(
      '/api/reverse?lat=' + encodeURIComponent(pt.lat) +
      '&lng=' + encodeURIComponent(pt.lng)
    );
    if (seq !== state.revSeq[kind]) return;
    const cur = state[kind];
    if (!cur || cur.label) return;

    const label = (r && r.label) ? String(r.label).trim() : '';
    if (!label) {
      renderPoints();
      return;
    }
    state[kind] = { lat: cur.lat, lng: cur.lng, label };
    setMarkerLabel(kind, label);
    renderPoints();
    updatePeek();
  } catch (err) {
    if (seq !== state.revSeq[kind]) return;
    console.warn('reverse geocode failed', err);
    renderPoints();
  }
}

function shortLabel(label) {
  if (!label) return '';
  const cut = label.split(',')[0].trim();
  return cut || label;
}

function setMarkerLabel(kind, label) {
  const m = state.markers[kind];
  if (!m) return;
  if (!label) {
    const old = m.getPopup();
    if (old) old.remove();
    return;
  }
  m.setPopup(new maplibregl.Popup({ offset: 18, closeButton: false }).setText(shortLabel(label)));
}

function clearPoint(kind) {
  state.revSeq[kind] = (state.revSeq[kind] || 0) + 1;
  state[kind] = null;
  state.estimate = null;
  state.measured = null;
  if (state.markers[kind]) {
    state.markers[kind].remove();
    state.markers[kind] = null;
  }
  if (map.getSource('route')) map.getSource('route').setData(emptyLine());
  state.routeCoords = null;
  state.active = kind;
  renderPoints();
  renderResult();
  updatePeek();
}

// removeStop drops the stop pin and its row; the route is re-quoted without it.
function removeStop(opts) {
  opts = opts || {};
  state.revSeq.stop = (state.revSeq.stop || 0) + 1;
  state.stop = null;
  state.stopOn = false;
  if (state.markers.stop) {
    state.markers.stop.remove();
    state.markers.stop = null;
  }
  if (state.active === 'stop') state.active = 'dest';
  renderPoints();
  if (!opts.skipEstimate) requestEstimate();
}

function addStop() {
  state.stopOn = true;
  state.active = 'stop';
  renderPoints();
  showToast('Tap the map to set the stop');
}

function clampWaiting(min) {
  return Number.isFinite(min) && min > 0 ? Math.min(min, 600) : 0;
}

function setWaiting(min) {
  state.waitingMin = clampWaiting(min);
  $('waiting-min').value = state.waitingMin;
}

function resetPins() {
  removeStop({ skipEstimate: true });
  clearPoint('pickup');
  clearPoint('dest');
  setWaiting(0);
  state.active = 'pickup';
  state.multiplier = 1;
  state.savedKey = null;
  setCardCollapsed(false);
  renderPoints();
  updateSaveButton();
}

let toastTimer = null;
function showToast(msg) {
  const el = $('toast');
  el.textContent = msg;
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, 2200);
}

function tripPlaceLabel(t, which) {
  const lat = t[which + '_lat'], lng = t[which + '_lng'];
  return t[which + '_label'] || lat.toFixed(5) + ', ' + lng.toFixed(5);
}

function tripHasStop(t) {
  return t.stop_lat != null && t.stop_lng != null;
}

function closeTripDetail() {
  state.viewingTrip = null;
  $('trip-detail').hidden = true;
  $('history-list-view').hidden = false;
  $('history-title').textContent = 'History';
}

function openTripDetail(t) {
  state.viewingTrip = t;
  const cur = state.cfg.rates.currency;
  $('history-list-view').hidden = true;
  $('trip-detail').hidden = false;
  $('history-title').textContent = 'Trip';
  $('detail-route').textContent = tripPlaceLabel(t, 'pickup') +
    (tripHasStop(t) ? ' → ' + tripPlaceLabel(t, 'stop') : '') +
    ' → ' + tripPlaceLabel(t, 'dest');
  $('detail-when').textContent = new Date(t.created_at).toLocaleString([], {
    weekday: 'short', month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit',
  });
  $('detail-km').textContent = t.distance_km.toFixed(2) + ' km';
  $('detail-min').textContent = Math.round(t.duration_min) + ' min';
  $('detail-suggested').textContent = cur + ' ' + t.suggested_fare;
  $('detail-charged').textContent = t.actual_charged != null ? cur + ' ' + t.actual_charged : '—';
  $('detail-wait-row').hidden = !(t.waiting_min > 0);
  $('detail-wait').textContent = Math.round(t.waiting_min) + ' min';
  const time = (iso) => new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  $('detail-recorded-row').hidden = !t.started_at;
  if (t.started_at) {
    const mins = Math.round((new Date(t.ended_at) - new Date(t.started_at)) / 60000);
    $('detail-recorded').textContent = time(t.started_at) + '–' + time(t.ended_at) + ' (' + mins + ' min)';
  }
  $('detail-quoted-row').hidden = t.quoted_fare == null;
  $('detail-quoted').textContent = t.quoted_fare != null ? cur + ' ' + t.quoted_fare : '';
  $('detail-mult').textContent = '×' + (t.multiplier > 0 ? t.multiplier : 1);
  $('detail-rates').textContent =
    cur + ' ' + t.rate_base + ' + ' + t.rate_per_km + '/km + ' + t.rate_per_min + '/min' +
    (t.rate_per_wait_min > 0 ? ' + ' + t.rate_per_wait_min + '/wait min' : '');
  const note = (t.note || '').trim();
  $('detail-note').hidden = !note;
  $('detail-note').textContent = note ? 'Note: ' + note : '';
}

// loadTripPins puts a saved trip's pins, stop, waiting time and multiplier back on
// the estimate screen without quoting.
function loadTripPins(t) {
  $('history').hidden = true;
  $('sheet-edit').hidden = true;
  closeTripDetail();
  setCardCollapsed(false);
  state.multiplier = t.multiplier > 0 ? t.multiplier : 1;
  setWaiting(t.waiting_min || 0);
  const skip = { skipEstimate: true };
  for (const kind of ['pickup', 'dest']) {
    setPoint(kind, {
      lat: t[kind + '_lat'],
      lng: t[kind + '_lng'],
      label: t[kind + '_label'] || undefined,
    }, skip);
  }
  if (tripHasStop(t)) {
    setPoint('stop', { lat: t.stop_lat, lng: t.stop_lng, label: t.stop_label || undefined }, skip);
  } else {
    removeStop(skip);
  }
}

function showTripOnMap(t) {
  loadTripPins(t);
  if (t.route_geometry) {
    state.estimate = {
      distance_km: t.distance_km,
      duration_min: t.duration_min,
      geometry: t.route_geometry,
      fare: t.suggested_fare,
    };
    state.savedKey = estimateKey(state.estimate);
    drawRoute(t.route_geometry);
    renderResult();
    showToast('Saved route');
    return;
  }
  requestEstimate();
  showToast('Pins loaded — quoting…');
}

function repeatTrip(t) {
  loadTripPins(t);
  requestEstimate();
  showToast('Pins loaded — quoting…');
}

function swapPoints() {
  [state.pickup, state.dest] = [state.dest, state.pickup];
  for (const kind of ['pickup', 'dest']) {
    const pt = state[kind];
    if (pt && !state.markers[kind]) {
      setPoint(kind, pt);
    } else if (pt) {
      state.markers[kind].setLngLat([pt.lng, pt.lat]);
    } else if (state.markers[kind]) {
      state.markers[kind].remove();
      state.markers[kind] = null;
    }
  }
  renderPoints();
  requestEstimate();
}

function fmtPoint(pt) {
  if (pt.label) return shortLabel(pt.label);
  return pt.lat.toFixed(5) + ', ' + pt.lng.toFixed(5);
}

function renderPoints() {
  for (const kind of PIN_KINDS) {
    const pt = state[kind];
    const label = $('label-' + kind);
    label.classList.remove('looking-up');
    if (pt) {
      label.textContent = fmtPoint(pt);
      label.classList.add('set');
      label.title = pt.label || (pt.lat.toFixed(5) + ', ' + pt.lng.toFixed(5));
    } else {
      label.textContent = PIN_PLACEHOLDERS[kind];
      label.classList.remove('set');
      label.title = '';
    }
    // The stop's ✕ also cancels a stop that hasn't been placed yet.
    if (kind !== 'stop') $('btn-clear-' + kind).hidden = !pt;
    const alreadySaved = !!(pt && pt.label && state.places.some((p) => p.label === pt.label));
    $('btn-star-' + kind).hidden = !pt || alreadySaved;
    $('row-' + kind).classList.toggle('active', state.active === kind && state.pickup && state.dest);
  }
  const both = !!(state.pickup && state.dest);
  $('row-stop').hidden = !state.stopOn;
  $('btn-add-stop').hidden = !both || state.stopOn || !!state.measured;
  $('waiting-wrap').hidden =
    !both || !!state.measured || !(state.cfg && state.cfg.rates.price_per_waiting_minute > 0);
}

let estimateTimer = null;
let estimateSeq = 0;

function requestEstimate() {
  if (state.rec || !state.pickup || !state.dest) return;
  clearTimeout(estimateTimer);
  estimateTimer = setTimeout(runEstimate, 250);
}

async function runEstimate() {
  if (state.measured) return runMeasure();
  const seq = ++estimateSeq;
  showResult('spinner');
  try {
    const est = await api('/api/estimate', {
      method: 'POST',
      body: JSON.stringify({
        pickup: { lat: state.pickup.lat, lng: state.pickup.lng },
        stop: state.stop ? { lat: state.stop.lat, lng: state.stop.lng } : undefined,
        dest: { lat: state.dest.lat, lng: state.dest.lng },
        waiting_min: state.waitingMin,
        multiplier: state.multiplier,
      }),
    });
    if (seq !== estimateSeq) return;
    state.estimate = est;
    state.savedKey = null;
    setCardCollapsed(false);
    drawRoute(est.geometry);
    renderResult();
  } catch (err) {
    if (seq !== estimateSeq) return;
    state.estimate = null;
    state.routeCoords = null;
    $('result-error-msg').textContent = err.message || 'Routing failed.';
    showResult('error');
    setCardCollapsed(false);
    updatePeek();
  }
}

function showResult(which) {
  $('result').hidden = false;
  $('result-spinner').hidden = which !== 'spinner';
  $('result-error').hidden = which !== 'error';
  $('result-body').hidden = which !== 'body';
}

function renderResult() {
  const est = state.estimate;
  if (!est) {
    $('result').hidden = true;
    updatePeek();
    return;
  }
  const m = state.measured;
  const cur = state.cfg.rates.currency;
  $('stat-km').textContent = est.distance_km.toFixed(1) + ' km';
  $('stat-min').textContent = Math.round(est.duration_min) + ' min' +
    (m && est.waiting_min >= 0.5 ? ' + ' + Math.round(est.waiting_min) + ' wait' : '');
  $('stat-fare').textContent = cur + ' ' + est.fare;
  $('stat-fare-label').textContent = m ? 'Metered' : 'Suggested';
  $('stat-quoted').hidden = !(m && m.quotedFare != null);
  $('stat-quoted').textContent = m && m.quotedFare != null ? 'quoted ' + cur + ' ' + m.quotedFare : '';
  for (const b of $('multipliers').children) {
    b.classList.toggle('on', Number(b.dataset.m) === state.multiplier);
  }
  showResult('body');
  updateSaveButton();
  updatePeek();
  requestAnimationFrame(updateCardOffset);
}

function estimateKey(est) {
  if (!est || !state.pickup || !state.dest) return null;
  return [
    state.pickup.lat.toFixed(5), state.pickup.lng.toFixed(5),
    state.stop ? state.stop.lat.toFixed(5) + ',' + state.stop.lng.toFixed(5) : '',
    state.dest.lat.toFixed(5), state.dest.lng.toFixed(5),
    est.distance_km.toFixed(3), est.fare, state.multiplier, state.waitingMin,
  ].join('|');
}

function updateSaveButton() {
  const btn = $('btn-save');
  if (!btn) return;
  const key = estimateKey(state.estimate);
  const already = key && key === state.savedKey;
  btn.disabled = !state.estimate || already || state.saving;
  btn.textContent = already ? 'Saved' : 'Save';
}

let pendingGeometry = null;

function drawRoute(encoded) {
  if (!encoded) return;
  if (!map.getSource('route')) {
    pendingGeometry = encoded;
    return;
  }
  pendingGeometry = null;
  const coords = finiteCoords(decodePolyline(encoded));
  if (!coords.length) return;
  state.routeCoords = coords;
  map.getSource('route').setData({
    type: 'Feature',
    geometry: { type: 'LineString', coordinates: coords },
  });
  fitRouteBounds(coords);
}

/** Decode a precision-5 Google polyline into [[lng, lat], …]. */
function decodePolyline(str) {
  const coords = [];
  let index = 0, lat = 0, lng = 0;
  while (index < str.length) {
    for (const which of [0, 1]) {
      let shift = 0, result = 0, byte;
      do {
        byte = str.charCodeAt(index++) - 63;
        result |= (byte & 0x1f) << shift;
        shift += 5;
      } while (byte >= 0x20);
      // Bitwise & (not &&) — sign flag for zigzag decode.
      const delta = (result & 1) ? ~(result >> 1) : (result >> 1);
      if (which === 0) lat += delta; else lng += delta;
    }
    coords.push([lng / 1e5, lat / 1e5]);
  }
  return coords;
}

async function loadPlaces() {
  state.places = [];
  if (state.user) {
    try {
      state.places = await api('/api/places');
    } catch (_) { /* no chips; the map still works */ }
  }
  renderChips();
  renderPoints();
}

function renderChips() {
  const bar = $('chips');
  bar.innerHTML = '';
  bar.hidden = state.places.length === 0;
  for (const p of state.places) {
    const chip = document.createElement('div');
    chip.className = 'chip';

    const use = document.createElement('button');
    use.type = 'button';
    use.className = 'chip-use';
    use.textContent = p.label;
    use.onclick = () => setPoint(nextPin(), { lat: p.lat, lng: p.lng, label: p.label });

    const x = document.createElement('button');
    x.type = 'button';
    x.className = 'chip-x';
    x.title = 'Remove place';
    x.setAttribute('aria-label', 'Remove ' + p.label);
    x.textContent = '×';
    x.onclick = () => removePlace(p);

    chip.appendChild(use);
    chip.appendChild(x);
    bar.appendChild(chip);
  }
}

function removePlace(p) {
  if (!confirm('Remove saved place "' + p.label + '"?')) return;
  api('/api/places/' + p.id, { method: 'DELETE' })
    .then(() => loadPlaces())
    .catch((err) => {
      if (err.status === 401) requireUser(() => removePlace(p));
      else alert(err.message);
    });
}

async function savePlace(kind) {
  const pt = state[kind];
  if (!pt) return;
  if (!state.user) return requireUser(() => savePlace(kind), 'Sign in to save places.');
  const label = prompt('Name this place:');
  if (!label || !label.trim()) return;
  try {
    const p = await api('/api/places', {
      method: 'POST',
      body: JSON.stringify({ label: label.trim(), lat: pt.lat, lng: pt.lng }),
    });
    state[kind] = { lat: p.lat, lng: p.lng, label: p.label };
    await loadPlaces();
  } catch (err) {
    if (err.status === 401) return requireUser(() => savePlace(kind));
    alert(err.message);
  }
}

function openSaveSheet() {
  if (!state.estimate || state.saving) return;
  if (!state.user) return requireUser(openSaveSheet, 'Sign in to save trips.');
  const key = estimateKey(state.estimate);
  if (key && key === state.savedKey) {
    showToast('Already saved');
    return;
  }
  $('save-amount').value = state.estimate.fare;
  $('save-note').value = '';
  $('sheet-save').hidden = false;
  $('save-amount').focus();
}

async function saveTrip() {
  if (state.saving || !state.estimate) return;
  const est = state.estimate;
  const key = estimateKey(est);
  if (key && key === state.savedKey) {
    $('sheet-save').hidden = true;
    showToast('Already saved');
    return;
  }

  const charged = parseFloat($('save-amount').value);
  const m = state.measured;
  const trip = {
    pickup_label: state.pickup.label || '',
    pickup_lat: state.pickup.lat,
    pickup_lng: state.pickup.lng,
    dest_label: state.dest.label || '',
    dest_lat: state.dest.lat,
    dest_lng: state.dest.lng,
    stop_label: state.stop ? state.stop.label || '' : '',
    stop_lat: state.stop ? state.stop.lat : undefined,
    stop_lng: state.stop ? state.stop.lng : undefined,
    waiting_min: m ? est.waiting_min : state.waitingMin,
    started_at: m ? m.startedAt : undefined,
    ended_at: m ? m.endedAt : undefined,
    track: m ? m.track : undefined,
    quoted_fare: m && m.quotedFare != null ? m.quotedFare : undefined,
    distance_km: est.distance_km,
    duration_min: est.duration_min,
    route_geometry: est.geometry,
    suggested_fare: est.fare,
    actual_charged: isNaN(charged) ? null : charged,
    multiplier: state.multiplier,
    note: $('save-note').value.trim(),
  };

  state.saving = true;
  const btn = $('btn-save-confirm');
  btn.disabled = true;
  $('btn-save').disabled = true;
  $('sheet-save').hidden = true;
  try {
    await api('/api/trips', { method: 'POST', body: JSON.stringify(trip) });
    state.savedKey = key;
    showToast('Trip saved');
    setCardCollapsed(true);
    updateSaveButton();
  } catch (err) {
    $('sheet-save').hidden = false;
    if (err.status === 401) requireUser(saveTrip); // session ended; the sheet keeps its values
    else alert(err.message);
  } finally {
    state.saving = false;
    btn.disabled = false;
    updateSaveButton();
  }
}

async function openHistory(opts) {
  opts = opts || {};
  if (!state.user) return requireUser(() => openHistory(opts), 'Sign in to see your trips.');
  $('history').hidden = false;
  if (!opts.keepDetail) closeTripDetail();

  const list = $('history-list');
  list.innerHTML = '';
  let trips;
  try {
    trips = await api('/api/trips');
  } catch (err) {
    if (err.status === 401) {
      $('history').hidden = true;
      return requireUser(() => openHistory(opts));
    }
    alert(err.message);
    return;
  }
  $('history-empty').hidden = trips.length > 0;

  const cur = state.cfg.rates.currency;
  const monthPrefix = new Date().toISOString().slice(0, 7);
  let count = 0, km = 0, chargedTotal = 0;
  for (const t of trips) {
    if (t.created_at.slice(0, 7) === monthPrefix) {
      count++;
      km += t.distance_km;
      chargedTotal += t.actual_charged != null ? t.actual_charged : 0;
    }
  }
  $('history-totals').innerHTML =
    '<div><b>' + count + '</b> trips</div>' +
    '<div><b>' + km.toFixed(1) + '</b> km</div>' +
    '<div><b>' + cur + ' ' + Math.round(chargedTotal) + '</b> charged</div>' +
    '<div class="muted" style="align-self:end">this month</div>';

  let viewing = null;
  for (const t of trips) {
    if (state.viewingTrip && t.id === state.viewingTrip.id) viewing = t;
    const li = document.createElement('li');
    const from = shortLabel(t.pickup_label) || t.pickup_lat.toFixed(3) + ',' + t.pickup_lng.toFixed(3);
    const to = shortLabel(t.dest_label) || t.dest_lat.toFixed(3) + ',' + t.dest_lng.toFixed(3);
    const when = new Date(t.created_at).toLocaleString([], {
      month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
    });
    const charged = t.actual_charged != null ? cur + ' ' + t.actual_charged : '—';
    li.innerHTML =
      '<div class="trip-row-top"><span class="trip-route"></span>' +
      '<span class="trip-charged">' + charged + '</span></div>' +
      '<div class="trip-row-bottom"><span>' + when + ' · ' + t.distance_km.toFixed(1) +
      ' km · ' + Math.round(t.duration_min) + ' min</span>' +
      '<span>suggested ' + t.suggested_fare + '</span></div>';
    li.querySelector('.trip-route').textContent = from + ' → ' + to;
    li.onclick = () => openTripDetail(t);
    list.appendChild(li);
  }
  if (opts.keepDetail && viewing) openTripDetail(viewing);
  else if (opts.keepDetail && state.viewingTrip) closeTripDetail();
}

function openEditSheet(t) {
  state.editingTripId = t.id;
  $('edit-summary').textContent =
    (t.pickup_label || 'map pin') + ' → ' + (t.dest_label || 'map pin') +
    ' · suggested ' + state.cfg.rates.currency + ' ' + t.suggested_fare;
  $('edit-amount').value = t.actual_charged != null ? t.actual_charged : '';
  $('edit-note').value = t.note || '';
  $('sheet-edit').hidden = false;
}

async function saveEdit() {
  const charged = parseFloat($('edit-amount').value);
  const patch = { note: $('edit-note').value.trim() };
  if (!isNaN(charged)) patch.actual_charged = charged;
  try {
    await api('/api/trips/' + state.editingTripId, {
      method: 'PATCH',
      body: JSON.stringify(patch),
    });
    $('sheet-edit').hidden = true;
    await openHistory({ keepDetail: !!state.viewingTrip });
  } catch (err) {
    if (err.status === 401) requireUser(saveEdit); // the edit sheet keeps its values
    else alert(err.message);
  }
}

function deleteTrip() {
  const id = state.editingTripId || (state.viewingTrip && state.viewingTrip.id);
  if (!id) return;
  if (!confirm('Delete this trip?')) return;
  api('/api/trips/' + id, { method: 'DELETE' })
    .then(() => {
      $('sheet-edit').hidden = true;
      state.viewingTrip = null;
      state.editingTripId = null;
      return openHistory();
    })
    .catch((err) => {
      if (err.status === 401) requireUser(deleteTrip);
      else alert(err.message);
    });
}

// ---- account ----
// Quotes are public. Anything personal goes through requireUser, which opens the
// sign-in sheet when needed and then carries on with what was asked for.

function setUser(u) {
  state.user = u && u.email ? { email: u.email } : null;
  const signedIn = !!state.user;
  $('account-label').hidden = signedIn;
  $('account-avatar').hidden = !signedIn;
  $('account-avatar').textContent = signedIn ? state.user.email[0] : '';
  $('btn-account').classList.toggle('signed-in', signedIn);
  $('btn-account').title = signedIn ? state.user.email : 'Sign in';
  $('account-email').textContent = signedIn ? state.user.email : '';
  if (!signedIn) closeAccountMenu();
}

function toggleAccountMenu() {
  if (!state.user) {
    openLogin('signin', 'Sign in to keep your trips and saved places.');
    return;
  }
  const open = $('account-menu').hidden;
  $('account-menu').hidden = !open;
  $('btn-account').setAttribute('aria-expanded', String(open));
}

function closeAccountMenu() {
  $('account-menu').hidden = true;
  $('btn-account').setAttribute('aria-expanded', 'false');
}

function requireUser(then, reason) {
  if (state.user) return then();
  state.afterLogin = then;
  openLogin('signin', reason || 'Your session ended. Sign in again to carry on.');
}

// The sign-in sheet's forms. repeat asks for the password twice: there is no
// other way to catch a typo in a new password. "sent" has no fields, only a
// message and a way back.
const LOGIN_MODES = {
  signin: { title: 'Sign in', submit: 'Sign in', email: true, password: 'current-password', links: true },
  signup: { title: 'Create an account', submit: 'Create account', email: true, password: 'new-password', repeat: true, links: true },
  forgot: { title: 'Reset your password', submit: 'Send reset link', email: true, links: true },
  sent: { title: 'Check your email', submit: 'Back to sign in' },
  reset: { title: 'Choose a new password', submit: 'Save password', password: 'new-password', repeat: true },
};

function openLogin(mode, reason) {
  closeAccountMenu();
  setLoginMode(mode, reason);
  $('sheet-login').hidden = false;
  // Start where typing is needed: the email, unless it's filled in and a password follows.
  const m = LOGIN_MODES[mode];
  if (m.email || m.password) {
    $(m.email && (!m.password || !$('login-email').value) ? 'login-email' : 'login-password').focus();
  }
}

function closeLogin() {
  state.afterLogin = null;
  $('sheet-login').hidden = true;
}

function setLoginMode(mode, reason) {
  const m = LOGIN_MODES[mode];
  state.loginMode = mode;
  if (reason !== undefined) $('login-reason').textContent = reason;
  $('login-title').textContent = m.title;
  $('btn-login').textContent = m.submit;
  $('login-email-wrap').hidden = !m.email;
  $('login-email').required = !!m.email;
  $('login-password-wrap').hidden = !m.password;
  $('login-password').required = !!m.password;
  $('login-password').autocomplete = m.password || 'off';
  $('login-hint').hidden = !m.repeat;
  $('login-repeat-wrap').hidden = !m.repeat;
  $('login-repeat').required = !!m.repeat;
  showPasswords(false);
  $('login-links').hidden = !m.links;
  $('btn-login-forgot').hidden = mode !== 'signin';
  const signupOpen = !!(state.cfg && state.cfg.signup);
  $('login-switch').hidden = mode === 'signin' && !signupOpen;
  const toSignin = mode !== 'signin';
  $('login-switch-text').textContent = mode === 'signup' ? 'Have an account?' : toSignin ? 'Remembered it?' : 'New here?';
  $('btn-login-switch').textContent = toSignin ? 'Sign in' : 'Create an account';
  $('login-error').hidden = true;
  $('login-password').value = '';
  $('login-repeat').value = '';
}

function showPasswords(show) {
  for (const id of ['login-password', 'login-repeat']) $(id).type = show ? 'text' : 'password';
  $('btn-show-password').textContent = show ? 'Hide' : 'Show';
  $('btn-show-password').setAttribute('aria-label', show ? 'Hide password' : 'Show password');
}

function loginError(msg) {
  $('login-error').textContent = msg;
  $('login-error').hidden = false;
}

async function submitLogin(e) {
  e.preventDefault();
  const mode = state.loginMode;
  if (mode === 'sent') return setLoginMode('signin', '');
  if (LOGIN_MODES[mode].repeat && $('login-password').value !== $('login-repeat').value) {
    return loginError('The passwords don\u2019t match.');
  }
  const email = $('login-email').value.trim();
  const password = $('login-password').value;
  const btn = $('btn-login');
  btn.disabled = true;
  try {
    if (mode === 'forgot') {
      await api('/api/password/forgot', { method: 'POST', body: JSON.stringify({ email }) });
      setLoginMode('sent', 'If ' + email + ' has an account, a link to choose a new password ' +
        'is on its way. It works once, for an hour. Check spam if it doesn\u2019t arrive.');
      return;
    }
    if (mode === 'reset') {
      // Every session ends with a reset, so the new password is used right away.
      const u = await api('/api/password/reset', {
        method: 'POST', body: JSON.stringify({ token: state.resetToken, password }),
      });
      state.resetToken = null;
      setUser(null);
      $('login-email').value = u.email;
      openLogin('signin', 'Password changed. Sign in with your new password.');
      return;
    }
    setUser(await api(mode === 'signup' ? '/api/signup' : '/api/login', {
      method: 'POST', body: JSON.stringify({ email, password }),
    }));
    $('sheet-login').hidden = true;
    if (mode === 'signup') showToast('Account created — welcome');
    await loadPlaces();
    const then = state.afterLogin;
    state.afterLogin = null;
    if (then) then();
  } catch (err) {
    loginError(err.message);
  } finally {
    btn.disabled = false;
  }
}

// openResetFromLink handles the emailed link (/?reset=TOKEN): the token is taken
// out of the address bar right away, then the "choose a new password" form opens.
function openResetFromLink() {
  const params = new URLSearchParams(location.search);
  const token = params.get('reset');
  if (!token) return;
  state.resetToken = token;
  params.delete('reset');
  history.replaceState(null, '', location.pathname + (params.size ? '?' + params : ''));
  openLogin('reset', '');
}

// signOut also ends a recording in progress and forgets a priced one, so whoever
// signs in next on this phone can't save another driver's trip as theirs.
async function signOut() {
  if (state.rec) {
    if (!confirm('Signing out discards the trip being recorded.')) return;
    stopRecording();
    state.rec = null;
    forgetStoredTrip();
    setRecordingUI(false);
  }
  state.measured = null;
  state.savedKey = null;
  try {
    await api('/api/logout', { method: 'POST' });
  } catch (_) { /* the cookie is cleared server-side or already gone */ }
  setUser(null);
  await loadPlaces();
  $('history').hidden = true;
  closeTripDetail();
  showToast('Signed out');
}

// ---- trip recording ----
// The browser collects GPS fixes while menged is on screen; the server turns them
// into distance, moving time, waiting time and a fare (/api/measure). The trip is
// kept in localStorage as it goes, so a reload or a crash doesn't lose it.

const TRIP_KEY = 'menged_trip';
const MIN_FIX_GAP_MS = 2000;    // keep at most one fix per 2 s
const LIVE_METER_MS = 30000;    // re-price the live meter this often (no routing call)

function storeTrip() {
  const r = state.rec;
  try {
    localStorage.setItem(TRIP_KEY, JSON.stringify({
      startedAt: r.startedAt, points: r.points, quotedFare: r.quotedFare, multiplier: r.multiplier,
    }));
  } catch (_) { /* storage full or blocked: keep recording in memory */ }
}

function forgetStoredTrip() {
  try { localStorage.removeItem(TRIP_KEY); } catch (_) {}
}

function startTrip() {
  if (state.rec) return;
  // Recording itself is local, but the trip can only be saved to an account.
  if (!state.user) return requireUser(startTrip, 'Sign in to record and save trips.');
  if (!navigator.geolocation) {
    showToast('No GPS on this device');
    return;
  }
  closePlaceSearch();
  state.rec = {
    startedAt: new Date().toISOString(),
    points: [],
    quotedFare: state.estimate && !state.measured ? state.estimate.fare : null,
    multiplier: state.multiplier,
  };
  storeTrip();
  beginRecording();
}

// resumeRecording picks up a trip that was recording when the page was closed.
function resumeRecording() {
  let saved = null;
  try { saved = JSON.parse(localStorage.getItem(TRIP_KEY) || 'null'); } catch (_) {}
  if (!saved || !saved.startedAt || !Array.isArray(saved.points)) return;
  state.rec = saved;
  state.multiplier = saved.multiplier || 1;
  beginRecording();
  liveMeasure();
  showToast('Recording resumed');
}

function beginRecording() {
  const r = state.rec;
  setRecordingUI(true);
  r.watchId = navigator.geolocation.watchPosition(onFix, onFixError, {
    enableHighAccuracy: true, maximumAge: 0, timeout: 20000,
  });
  r.timer = setInterval(tickMeter, 1000);
  acquireWakeLock();
  drawTrack();
  tickMeter();
}

function stopRecording() {
  const r = state.rec;
  if (!r) return;
  if (r.watchId != null) navigator.geolocation.clearWatch(r.watchId);
  clearInterval(r.timer);
  if (r.wakeLock) r.wakeLock.release().catch(() => {});
  r.watchId = null;
  r.wakeLock = null;
}

async function acquireWakeLock() {
  const r = state.rec;
  if (!r || !navigator.wakeLock || document.visibilityState !== 'visible') return;
  try {
    r.wakeLock = await navigator.wakeLock.request('screen');
  } catch (_) { /* battery saver or unsupported; recording still works while on screen */ }
}

// The OS drops the wake lock whenever the page is hidden; take it again on return.
document.addEventListener('visibilitychange', () => {
  if (state.rec && document.visibilityState === 'visible') {
    acquireWakeLock();
    showToast('Recording');
  }
});

function onFix(pos) {
  const r = state.rec;
  if (!r) return;
  const p = {
    lat: pos.coords.latitude,
    lng: pos.coords.longitude,
    t: pos.timestamp || Date.now(),
    acc: Math.round(pos.coords.accuracy || 0),
  };
  const last = r.points[r.points.length - 1];
  if (last && p.t - last.t < MIN_FIX_GAP_MS) return;
  r.points.push(p);
  storeTrip();
  $('meter-note').textContent = p.acc > 50
    ? 'GPS is rough (±' + p.acc + ' m) — short moves won’t count until it improves.'
    : 'Keep menged on screen — recording pauses when it isn’t.';
  drawTrack();
  if (Date.now() > state.followUntil) map.easeTo({ center: [p.lng, p.lat], duration: 600 });
  if (r.points.length === 2 || !r.lastMeasureAt || Date.now() - r.lastMeasureAt > LIVE_METER_MS) {
    liveMeasure();
  }
}

function onFixError(err) {
  if (err && err.code === 1) {
    stopRecording();
    state.rec = null;
    forgetStoredTrip();
    setRecordingUI(false);
    showToast('Location permission denied — recording stopped');
    return;
  }
  $('meter-note').textContent = 'Waiting for GPS…';
}

async function liveMeasure() {
  const r = state.rec;
  if (!r || !r.points.length) return;
  r.lastMeasureAt = Date.now();
  try {
    r.live = await api('/api/measure', {
      method: 'POST',
      body: JSON.stringify({ track: r.points, multiplier: r.multiplier, route: false }),
    });
    tickMeter();
  } catch (_) { /* keep the last reading; the final price is computed at the end */ }
}

function tickMeter() {
  const r = state.rec;
  if (!r) return;
  const sec = Math.max(0, Math.floor((Date.now() - new Date(r.startedAt)) / 1000));
  const h = Math.floor(sec / 3600), m = Math.floor(sec / 60) % 60, s = sec % 60;
  const pad = (n) => String(n).padStart(2, '0');
  $('meter-elapsed').textContent = (h ? h + ':' + pad(m) : m) + ':' + pad(s);
  const live = r.live;
  $('meter-km').textContent = (live ? live.distance_km : 0).toFixed(1) + ' km';
  $('meter-wait').textContent = Math.round(live ? live.waiting_min : 0) + ' min waiting';
  $('meter-fare').textContent = live ? state.cfg.rates.currency + ' ' + live.fare : '–';
}

function drawTrack() {
  const src = map && map.getSource('track');
  if (!src) return;
  const pts = state.rec ? state.rec.points : [];
  src.setData({
    type: 'Feature',
    geometry: { type: 'LineString', coordinates: pts.map((p) => [p.lng, p.lat]) },
  });
}

function setRecordingUI(on) {
  $('points').hidden = on;
  $('search-wrap').hidden = on || !(state.cfg && state.cfg.geocode);
  $('meter').hidden = !on;
  $('btn-navigate').hidden = !(on && state.dest);
  $('btn-end-trip').disabled = false;
  if (on) {
    $('result').hidden = true;
    setCardCollapsed(false);
  } else {
    drawTrack();
    renderResult();
  }
  requestAnimationFrame(updateCardOffset);
}

async function endTrip() {
  const r = state.rec;
  if (!r) return;
  if (!r.points.length) {
    showToast('No GPS fix yet');
    return;
  }
  stopRecording();
  $('btn-end-trip').disabled = true;
  $('meter-note').textContent = 'Pricing the trip…';
  let m;
  try {
    m = await api('/api/measure', {
      method: 'POST',
      body: JSON.stringify({ track: r.points, multiplier: r.multiplier, route: true }),
    });
  } catch (err) {
    // Nothing is lost: the fixes are still stored. Keep recording; End again or Discard.
    beginRecording();
    alert('Could not price the trip: ' + err.message +
      '\nStill recording — tap End trip to try again, or Discard.');
    return;
  }

  forgetStoredTrip();
  state.rec = null;
  setRecordingUI(false);
  state.multiplier = r.multiplier;
  const skip = { skipEstimate: true };
  removeStop(skip);
  // A recorded trip starts and ends where the GPS says, not where pins were planned.
  setPoint('pickup', m.start, skip);
  setPoint('dest', m.end, skip);
  state.measured = {
    startedAt: r.startedAt,
    endedAt: new Date().toISOString(),
    points: r.points,
    track: m.track,
    quotedFare: r.quotedFare,
  };
  showMeasured(m);
  showToast(m.rough ? 'Trip priced from rough GPS — check the distance'
    : m.method === 'road' ? 'Trip priced' : 'Trip priced from GPS (routing unavailable)');
}

function showMeasured(m) {
  state.estimate = {
    distance_km: m.distance_km,
    duration_min: m.duration_min,
    waiting_min: m.waiting_min,
    geometry: m.geometry,
    fare: m.fare,
    breakdown: m.breakdown,
  };
  state.savedKey = null;
  setCardCollapsed(false);
  drawRoute(m.geometry);
  renderPoints();
  renderResult();
}

// runMeasure re-prices a finished recording, e.g. after a multiplier change. The
// route through the track is cached server-side, so this costs no routing quota.
async function runMeasure() {
  const seq = ++estimateSeq;
  showResult('spinner');
  try {
    const m = await api('/api/measure', {
      method: 'POST',
      body: JSON.stringify({ track: state.measured.points, multiplier: state.multiplier, route: true }),
    });
    if (seq !== estimateSeq || !state.measured) return;
    showMeasured(m);
  } catch (err) {
    if (seq !== estimateSeq) return;
    $('result-error-msg').textContent = err.message || 'Pricing failed.';
    showResult('error');
  }
}

function discardTrip() {
  if (!state.rec || !confirm('Discard this recording?')) return;
  stopRecording();
  state.rec = null;
  forgetStoredTrip();
  setRecordingUI(false);
}

// quoteText is the one-line quote sent to a passenger, e.g.
// "Bole → Ayat, 14.0 km, ~35 min, ETB 240". Unlabelled pins drop the place names.
function quoteText() {
  const est = state.estimate;
  const names = [state.pickup, state.stop, state.dest]
    .filter(Boolean)
    .map((p) => (p.label ? shortLabel(p.label) : ''));
  const parts = [];
  if (names.every(Boolean)) parts.push(names.join(' → '));
  parts.push(est.distance_km.toFixed(1) + ' km', '~' + Math.round(est.duration_min) + ' min');
  const wait = state.measured ? Math.round(est.waiting_min) : state.waitingMin;
  if (wait > 0) parts.push('+' + wait + ' min waiting');
  parts.push(state.cfg.rates.currency + ' ' + est.fare);
  return parts.join(', ');
}

// mapsLink is a Google Maps directions URL for the current pins (Maps URLs, no API
// key). Shared quotes show the route from pickup; navigate starts turn-by-turn from
// the device instead. Google's route can differ slightly from ours; the fare is ours.
function mapsLink(navigate) {
  const ll = (p) => p.lat.toFixed(6) + ',' + p.lng.toFixed(6);
  const q = new URLSearchParams({ api: '1', destination: ll(state.dest), travelmode: 'driving' });
  if (navigate) q.set('dir_action', 'navigate');
  else q.set('origin', ll(state.pickup));
  if (state.stop) q.set('waypoints', ll(state.stop));
  return 'https://www.google.com/maps/dir/?' + q.toString();
}

// shareQuote always copies the quote, then opens the share sheet on phones. Desktop
// browsers also expose navigator.share, but there it opens a menu instead of copying.
async function shareQuote() {
  if (!state.estimate) return;
  const text = quoteText() + '\n' + mapsLink(false);
  let copied = false;
  try {
    await navigator.clipboard.writeText(text);
    copied = true;
  } catch (_) { /* no clipboard access; the share sheet or prompt below still works */ }

  const phone = window.matchMedia('(pointer: coarse)').matches;
  if (phone && navigator.share) {
    try {
      await navigator.share({ text });
      return;
    } catch (_) { /* dismissed or unsupported; fall through */ }
  }
  if (copied) showToast('Quote copied');
  else prompt('Copy this quote:', text);
}

$('card-handle').onclick = (e) => {
  e.stopPropagation();
  toggleCardCollapsed();
};
$('card-peek').onclick = (e) => {
  e.stopPropagation();
  setCardCollapsed(false);
};

$('btn-recenter').onclick = (e) => {
  e.stopPropagation();
  recenterMap();
};

$('btn-locate').onclick = () => {
  if (!navigator.geolocation) {
    showToast('No GPS on this device');
    return;
  }
  const btn = $('btn-locate');
  if (btn.classList.contains('busy')) return;
  btn.classList.add('busy');
  btn.title = 'Locating…';
  navigator.geolocation.getCurrentPosition(
    (pos) => {
      btn.classList.remove('busy');
      btn.title = 'Use my location';
      const { latitude: lat, longitude: lng, accuracy } = pos.coords;
      setPoint('pickup', { lat, lng });
      showAccuracyCircle(lng, lat, accuracy);
      const zoom = accuracyZoom(accuracy);
      map.flyTo({ center: [lng, lat], zoom });
      const metres = Math.round(accuracy);
      showToast(metres > 0 ? 'Located ±' + metres + ' m' : 'Located');
    },
    (err) => {
      btn.classList.remove('busy');
      btn.title = 'Use my location';
      clearAccuracyCircle();
      const msg = err && err.code === 1
        ? 'Location permission denied'
        : 'Could not read GPS';
      showToast(msg);
    },
    { enableHighAccuracy: true, timeout: 12000, maximumAge: 5000 }
  );
};

function accuracyZoom(accuracy) {
  if (!accuracy || accuracy > 2000) return 13;
  if (accuracy > 500) return 14;
  if (accuracy > 100) return 15;
  return 16;
}

function showAccuracyCircle(lng, lat, accuracy) {
  const src = map.getSource('accuracy');
  if (!src || !(accuracy > 0)) {
    clearAccuracyCircle();
    return;
  }
  src.setData({
    type: 'FeatureCollection',
    features: [{
      type: 'Feature',
      properties: {},
      geometry: { type: 'Polygon', coordinates: [circleRing(lng, lat, accuracy)] },
    }],
  });
}

function clearAccuracyCircle() {
  const src = map.getSource('accuracy');
  if (src) src.setData({ type: 'FeatureCollection', features: [] });
}

function circleRing(lng, lat, radiusM, steps = 64) {
  const coords = [];
  const latRad = lat * Math.PI / 180;
  const mPerDegLat = 111320;
  const mPerDegLng = 111320 * Math.cos(latRad);
  if (!(mPerDegLng > 1e-6) || !Number.isFinite(radiusM)) return coords;
  for (let i = 0; i <= steps; i++) {
    const t = (i / steps) * 2 * Math.PI;
    const x = lng + (radiusM * Math.cos(t)) / mPerDegLng;
    const y = lat + (radiusM * Math.sin(t)) / mPerDegLat;
    if (Number.isFinite(x) && Number.isFinite(y)) coords.push([x, y]);
  }
  return coords;
}

function setupPlaceSearch() {
  const input = $('place-search');
  const overlay = $('search-overlay');
  let timer = null;
  let seq = 0;

  $('btn-open-search').onclick = (e) => {
    e.stopPropagation();
    openPlaceSearch();
  };
  $('btn-close-search').onclick = (e) => {
    e.stopPropagation();
    closePlaceSearch();
  };
  overlay.addEventListener('click', (e) => {
    if (e.target === overlay) closePlaceSearch();
  });
  overlay.querySelector('.search-panel').addEventListener('click', (e) => e.stopPropagation());

  input.addEventListener('input', () => {
    clearTimeout(timer);
    const q = input.value.trim();
    if (q.length < 2) {
      clearSearchResults();
      setSearchHint('Type at least 2 characters');
      return;
    }
    setSearchHint('Searching…');
    timer = setTimeout(() => runPlaceSearch(q, ++seq), 300);
  });
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closePlaceSearch();
  });

  async function runPlaceSearch(q, mySeq) {
    const near = state.pickup || state.dest || state.cfg.map_center;
    const local = matchSavedPlaces(q);
    let remote = [];
    try {
      remote = await api(
        '/api/geocode?q=' + encodeURIComponent(q) +
        '&lat=' + encodeURIComponent(near.lat) +
        '&lng=' + encodeURIComponent(near.lng)
      ) || [];
    } catch (_) { /* show local matches only */ }
    if (mySeq !== seq) return;
    const seen = new Set(local.map((p) => p.label.toLowerCase()));
    const merged = local.concat(
      remote.filter((h) => h.label && !seen.has(h.label.toLowerCase()))
    ).slice(0, 8);
    renderSearchResults(merged);
  }
}

function openPlaceSearch() {
  const overlay = $('search-overlay');
  overlay.hidden = false;
  $('place-search').value = '';
  clearSearchResults();
  setSearchHint('Type at least 2 characters');
  requestAnimationFrame(() => $('place-search').focus());
}

function closePlaceSearch() {
  const overlay = $('search-overlay');
  if (!overlay || overlay.hidden) return;
  overlay.hidden = true;
  $('place-search').value = '';
  clearSearchResults();
}

function matchSavedPlaces(q) {
  const needle = q.toLowerCase();
  return state.places
    .filter((p) => p.label && p.label.toLowerCase().includes(needle))
    .map((p) => ({ label: p.label, lat: p.lat, lng: p.lng, saved: true }));
}

function setSearchHint(text) {
  const hint = $('search-hint');
  if (!hint) return;
  if (!text) {
    hint.hidden = true;
    hint.textContent = '';
    return;
  }
  hint.hidden = false;
  hint.textContent = text;
}

function renderSearchResults(hits) {
  const list = $('search-results');
  list.innerHTML = '';
  if (!hits.length) {
    setSearchHint('No places found');
    return;
  }
  setSearchHint('');
  for (const h of hits) {
    const li = document.createElement('li');
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'search-hit';
    btn.innerHTML = '<span class="search-hit-label"></span>' +
      (h.saved ? '<span class="search-hit-tag">saved</span>' : '');
    btn.querySelector('.search-hit-label').textContent = h.label;
    btn.onclick = () => {
      setPoint(nextPin(), { lat: h.lat, lng: h.lng, label: h.label });
      map.flyTo({ center: [h.lng, h.lat], zoom: 15 });
      closePlaceSearch();
    };
    li.appendChild(btn);
    list.appendChild(li);
  }
}

function clearSearchResults() {
  $('search-results').innerHTML = '';
}

$('btn-swap').onclick = swapPoints;
$('btn-reset').onclick = (e) => { e.stopPropagation(); resetPins(); };
$('btn-clear-pickup').onclick = (e) => { e.stopPropagation(); clearPoint('pickup'); };
$('btn-clear-dest').onclick = (e) => { e.stopPropagation(); clearPoint('dest'); };
$('btn-star-pickup').onclick = (e) => { e.stopPropagation(); savePlace('pickup'); };
$('btn-star-dest').onclick = (e) => { e.stopPropagation(); savePlace('dest'); };
$('btn-clear-stop').onclick = (e) => { e.stopPropagation(); removeStop(); };
$('btn-star-stop').onclick = (e) => { e.stopPropagation(); savePlace('stop'); };
$('btn-add-stop').onclick = (e) => { e.stopPropagation(); addStop(); };
$('row-pickup').onclick = () => { state.active = 'pickup'; renderPoints(); };
$('row-stop').onclick = () => { state.active = 'stop'; renderPoints(); };
$('row-dest').onclick = () => { state.active = 'dest'; renderPoints(); };

// Don't write the value back while typing, so the field can be cleared and retyped.
$('waiting-min').addEventListener('input', () => {
  state.waitingMin = clampWaiting(parseFloat($('waiting-min').value));
  requestEstimate();
});
$('waiting-min').addEventListener('change', () => setWaiting(state.waitingMin));

$('multipliers').onclick = (e) => {
  const b = e.target.closest('button');
  if (!b) return;
  state.multiplier = Number(b.dataset.m);
  renderResult();
  requestEstimate();
};

$('btn-retry').onclick = runEstimate;
$('btn-share').onclick = shareQuote;
$('btn-start-trip').onclick = (e) => { e.stopPropagation(); startTrip(); };
$('btn-end-trip').onclick = endTrip;
$('btn-cancel-trip').onclick = discardTrip;
$('btn-navigate').onclick = () => {
  if (!state.dest) return;
  // menged gets no GPS while it's in the background, so the meter has a gap until return.
  showToast('Recording pauses while menged is in the background');
  window.open(mapsLink(true), '_blank', 'noopener');
};
$('btn-save').onclick = openSaveSheet;
$('btn-save-cancel').onclick = () => { $('sheet-save').hidden = true; };
$('btn-save-confirm').onclick = saveTrip;

$('btn-account').onclick = (e) => {
  e.stopPropagation();
  toggleAccountMenu();
};
$('menu-history').onclick = () => {
  closeAccountMenu();
  openHistory();
};
$('menu-logout').onclick = () => {
  closeAccountMenu();
  signOut();
};
document.addEventListener('click', (e) => {
  if (!$('account-menu').hidden && !$('account-menu').contains(e.target)) closeAccountMenu();
});
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  if (!$('sheet-login').hidden) closeLogin();
  closeAccountMenu();
});

$('login-form').addEventListener('submit', submitLogin);
$('btn-login-close').onclick = closeLogin;
$('btn-show-password').onclick = () => showPasswords($('login-password').type === 'password');
$('btn-login-switch').onclick = () => {
  setLoginMode(state.loginMode === 'signin' ? 'signup' : 'signin', '');
  $('login-email').focus();
};
$('btn-login-forgot').onclick = () => {
  setLoginMode('forgot', 'Enter your email and we\u2019ll send a link to choose a new password.');
  $('login-email').focus();
};
$('btn-history-back').onclick = () => {
  if (!$('trip-detail').hidden) {
    closeTripDetail();
    return;
  }
  $('history').hidden = true;
};
$('btn-detail-map').onclick = () => {
  if (state.viewingTrip) showTripOnMap(state.viewingTrip);
};
$('btn-detail-repeat').onclick = () => {
  if (state.viewingTrip) repeatTrip(state.viewingTrip);
};
$('btn-detail-edit').onclick = () => {
  if (state.viewingTrip) openEditSheet(state.viewingTrip);
};
$('btn-detail-delete').onclick = () => {
  if (!state.viewingTrip) return;
  state.editingTripId = state.viewingTrip.id;
  deleteTrip();
};
$('btn-edit-cancel').onclick = () => { $('sheet-edit').hidden = true; };
$('btn-edit-save').onclick = saveEdit;
$('btn-edit-delete').onclick = deleteTrip;

init().catch((err) => alert('Could not start: ' + err.message));
