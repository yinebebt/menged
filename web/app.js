'use strict';

const $ = (id) => document.getElementById(id);

const state = {
  cfg: null,
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
  if (!resp.ok) {
    let msg = resp.statusText;
    try { msg = (await resp.json()).error || msg; } catch (_) {}
    throw new Error(msg);
  }
  return resp.status === 204 ? null : resp.json();
}

let map;

async function init() {
  state.cfg = await api('/api/config');
  $('save-currency').textContent = state.cfg.rates.currency;
  $('waiting-wrap').hidden = !(state.cfg.rates.price_per_waiting_minute > 0);

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
    if (pendingGeometry) drawRoute(pendingGeometry);
  });

  map.on('click', (e) => {
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
  $('row-stop').hidden = !state.stopOn;
  $('point-extras').hidden = !(state.pickup && state.dest);
  $('btn-add-stop').hidden = state.stopOn;
}

let estimateTimer = null;
let estimateSeq = 0;

function requestEstimate() {
  if (!state.pickup || !state.dest) return;
  clearTimeout(estimateTimer);
  estimateTimer = setTimeout(runEstimate, 250);
}

async function runEstimate() {
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
  $('stat-km').textContent = est.distance_km.toFixed(1) + ' km';
  $('stat-min').textContent = Math.round(est.duration_min) + ' min';
  $('stat-fare').textContent = state.cfg.rates.currency + ' ' + est.fare;
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
  try {
    state.places = await api('/api/places');
  } catch (_) {
    state.places = [];
  }
  renderChips();
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
    x.onclick = () => {
      if (!confirm('Remove saved place "' + p.label + '"?')) return;
      api('/api/places/' + p.id, { method: 'DELETE' })
        .then(() => loadPlaces())
        .catch((err) => alert(err.message));
    };

    chip.appendChild(use);
    chip.appendChild(x);
    bar.appendChild(chip);
  }
}

async function savePlace(kind) {
  const pt = state[kind];
  if (!pt) return;
  const label = prompt('Name this place:');
  if (!label || !label.trim()) return;
  try {
    const p = await api('/api/places', {
      method: 'POST',
      body: JSON.stringify({ label: label.trim(), lat: pt.lat, lng: pt.lng }),
    });
    state[kind] = { lat: p.lat, lng: p.lng, label: p.label };
    await loadPlaces();
    renderPoints();
  } catch (err) {
    alert(err.message);
  }
}

function openSaveSheet() {
  if (!state.estimate || state.saving) return;
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
    waiting_min: state.waitingMin,
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
    alert(err.message);
  } finally {
    state.saving = false;
    btn.disabled = false;
    updateSaveButton();
  }
}

async function openHistory(opts) {
  opts = opts || {};
  $('history').hidden = false;
  if (!opts.keepDetail) closeTripDetail();

  const list = $('history-list');
  list.innerHTML = '';
  let trips;
  try {
    trips = await api('/api/trips');
  } catch (err) {
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
    alert(err.message);
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
    .catch((err) => alert(err.message));
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
  if (state.waitingMin > 0) parts.push('+' + state.waitingMin + ' min waiting');
  parts.push(state.cfg.rates.currency + ' ' + est.fare);
  return parts.join(', ');
}

// routeLink opens the same pickup, stop and destination in Google Maps (Maps URLs,
// no API key). Its route can differ slightly from ours; the quoted fare is ours.
function routeLink() {
  const ll = (p) => p.lat.toFixed(6) + ',' + p.lng.toFixed(6);
  const q = new URLSearchParams({
    api: '1',
    origin: ll(state.pickup),
    destination: ll(state.dest),
    travelmode: 'driving',
  });
  if (state.stop) q.set('waypoints', ll(state.stop));
  return 'https://www.google.com/maps/dir/?' + q.toString();
}

async function shareQuote() {
  if (!state.estimate) return;
  const text = quoteText() + '\n' + routeLink();
  if (navigator.share) {
    try {
      await navigator.share({ text });
      return;
    } catch (err) {
      if (err && err.name === 'AbortError') return;
    }
  }
  try {
    await navigator.clipboard.writeText(text);
    showToast('Quote copied');
  } catch (_) {
    prompt('Copy this quote:', text);
  }
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
$('btn-save').onclick = openSaveSheet;
$('btn-save-cancel').onclick = () => { $('sheet-save').hidden = true; };
$('btn-save-confirm').onclick = saveTrip;

$('btn-history').onclick = () => openHistory();
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
