# public-terminal-heterogeneous-dates

Scaffold-only smoke workload derived from Terminal-Bench `heterogeneous-dates`.

The task keeps the public objective and numeric oracle: calculate the average
daily high-minus-low temperature difference for the San Francisco CSV files and
write only the number to `avg_temp.txt`. The PCS-specific adaptation is the
execution substrate: the dataset is modeled as server-owned context, while the
driver invokes Codex CLI and records JSONL token usage.

This workload is not part of the current 5-workload main experiment table.
