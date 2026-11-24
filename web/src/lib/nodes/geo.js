// Where a node sits on the fleet map, read from the flag in the name
// the operator gave it. There is no geo data in the panel and none is
// fetched: a name without a known flag simply gets no marker, and the
// node's card below the map still says everything about it.
//
// Positions are percentages of the map panel. The projection is fitted
// to the dotted world the design draws (a 100x50 equirectangular field
// cropped by the panel), not to a map library: x grows with longitude,
// y shrinks with latitude, and both are clamped inside the panel.
const PLACES = {
  '🇹🇷': [41.0, 29.0], // Istanbul
  '🇸🇪': [59.3, 18.1], // Stockholm
  '🇳🇱': [52.4, 4.9], // Amsterdam
  '🇩🇪': [50.1, 8.7], // Frankfurt
  '🇫🇮': [60.2, 24.9], // Helsinki
  '🇫🇷': [48.9, 2.3], // Paris
  '🇬🇧': [51.5, -0.1], // London
  '🇺🇸': [39.0, -98.0], // United States
  '🇨🇦': [43.7, -79.4], // Toronto
  '🇦🇺': [-33.9, 151.2], // Sydney
  '🇸🇬': [1.35, 103.8], // Singapore
  '🇯🇵': [35.7, 139.7], // Tokyo
  '🇮🇳': [19.1, 72.9], // Mumbai
  '🇧🇷': [-23.5, -46.6], // São Paulo
  '🇦🇪': [25.2, 55.3], // Dubai
  '🇷🇺': [55.8, 37.6], // Moscow
  '🇵🇱': [52.2, 21.0], // Warsaw
  '🇮🇹': [41.9, 12.5], // Rome
  '🇪🇸': [40.4, -3.7], // Madrid
  '🇨🇭': [47.4, 8.5], // Zurich
  '🇦🇹': [48.2, 16.4], // Vienna
  '🇳🇴': [59.9, 10.7], // Oslo
  '🇩🇰': [55.7, 12.6], // Copenhagen
  '🇮🇷': [35.7, 51.4], // Tehran
  '🇹🇭': [13.8, 100.5], // Bangkok
  '🇰🇷': [37.6, 127.0], // Seoul
  '🇭🇰': [22.3, 114.2], // Hong Kong
  '🇮🇩': [-6.2, 106.8], // Jakarta
  '🇻🇳': [21.0, 105.8], // Hanoi
  '🇲🇾': [3.1, 101.7], // Kuala Lumpur
  '🇵🇭': [14.6, 121.0], // Manila
  '🇳🇿': [-36.8, 174.8], // Auckland
  '🇿🇦': [-26.2, 28.0], // Johannesburg
  '🇪🇬': [30.0, 31.2], // Cairo
}

const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, v))

// The node's place on the map, or null when its name carries no flag
// this table knows.
export function nodePlace(name) {
  if (!name) return null
  for (const flag of Object.keys(PLACES)) {
    if (name.includes(flag)) {
      const [lat, lon] = PLACES[flag]
      return {
        x: clamp(47.4 + 0.315 * lon, 3, 97),
        y: clamp(82.3 - 1.1 * lat, 6, 94),
      }
    }
  }
  return null
}
