const EARTH_RADIUS_MILES = 3958.8

export function haversineMiles(lat1: number, lon1: number, lat2: number, lon2: number): number {
  const toRad = (d: number) => (d * Math.PI) / 180
  const dLat = toRad(lat2 - lat1)
  const dLon = toRad(lon2 - lon1)
  const a = Math.sin(dLat / 2) ** 2 + Math.cos(toRad(lat1)) * Math.cos(toRad(lat2)) * Math.sin(dLon / 2) ** 2
  return 2 * EARTH_RADIUS_MILES * Math.asin(Math.sqrt(a))
}

export function pathMiles(points: { lat?: number; lon?: number }[]): number {
  let total = 0
  let prev: { lat: number; lon: number } | null = null
  for (const p of points) {
    if (p.lat === undefined || p.lon === undefined) continue
    if (prev) total += haversineMiles(prev.lat, prev.lon, p.lat, p.lon)
    prev = { lat: p.lat, lon: p.lon }
  }
  return total
}
