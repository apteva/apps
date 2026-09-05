export type Network = { shape: number[]; weights: number[][][]; step: number };

export function forward(network: Network, x: number, y: number): number[][] {
  const layers = [[x, y]];
  network.weights.forEach((rows, l) => layers.push(rows.map(row => {
    const z = row[row.length - 1] + layers[l].reduce((sum, v, i) => sum + v * row[i], 0);
    return l === network.weights.length - 1 ? 1 / (1 + Math.exp(-z)) : Math.tanh(z);
  })));
  return layers;
}

// Leaky integrate-and-fire encoding of dense-network activations. These
// spikes are a visualization, not the units used by backpropagation.
export function spikeStep(voltage: number, activation: number, dt: number) {
  const next = voltage + dt * (-voltage + 2.4 * Math.abs(activation)) / 0.18;
  return { voltage: next >= 1 ? 0 : Math.max(0, next), fired: next >= 1 };
}
