// nvidia-smi answers with the vendor in front of every card: "NVIDIA GeForce
// RTX 3060", "NVIDIA A100-SXM4-40GB". This app reads NVIDIA cards and nothing
// else (arch/02), the section is headed GPU, and the figure came from
// nvidia-smi — so the prefix is seven characters that say what the reader
// already knows, and on a narrow tile they are the seven characters that push
// the model number off the end. The model number is the part that identifies
// the card, and it is at the end.
//
// Display only. The value stays whole in the data and in every tooltip, because
// that is the string somebody pastes into a search or a bug report.
export function shortGPUName(name: string): string {
  return name.replace(/^NVIDIA\s+/i, '')
}
