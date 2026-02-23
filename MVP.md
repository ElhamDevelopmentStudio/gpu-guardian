
# 🔥 GPU Stress Guardian — 2-Day MVP

## 🎯 MVP Goal

Build a CLI tool in Go that:

1.  Monitors GPU telemetry (temp, VRAM, utilization)
    
2.  Measures XTTS throughput
    
3.  Dynamically adjusts concurrency
    
4.  Prevents sustained 4–5× slowdown
    
5.  Enforces temperature ceiling
    
6.  Logs everything
    

No daemon.  
No npm.  
No pip.  
No RL.  
No simulation.

Just the control loop.

----------

# 🧱 What This MVP Will Actually Do

You run:

guardian control --cmd  "python generate_xtts.py"

The tool:

-   Spawns your XTTS process
    
-   Monitors GPU every 2 seconds
    
-   Measures throughput (chars/sec or audio-sec/sec)
    
-   Adjusts concurrency via environment variable or flag
    
-   Reduces load if:
    
    -   Temp > soft threshold
        
    -   Throughput drops below 70% baseline
        
-   Pauses briefly if needed
    
-   Logs to file
    

That’s it.

----------

# 🏗 Architecture (MVP)

main.go  
 ├── telemetry.go  
 ├── controller.go  
 ├── adapter_xtts.go  
 ├── throughput.go  
 └── logger.go

----------

# 🧠 Control Strategy (Ultra Simple but Effective)

No fancy PID.

Just rule-based:

### Every 2 seconds:

1.  Read:
    
    -   GPU Temp
        
    -   VRAM %
        
    -   Utilization
        
    -   Throughput
        
2.  If Temp > HARD_LIMIT:  
    → Reduce concurrency by 1
    
3.  If Temp > SOFT_LIMIT and rising:  
    → Reduce concurrency by 1
    
4.  If Throughput < 70% baseline for 30 seconds:  
    → Reduce concurrency by 1
    
5.  If Temp stable and Throughput healthy:  
    → Increase concurrency by 1 (max cap)
    

Add:

-   10s cooldown between adjustments (anti-oscillation)
    

That’s enough to prove concept.

----------

# 🧩 Required Components (Minimal)

----------

## 1️⃣ Telemetry (Linux + NVIDIA)

Use `nvidia-smi` shell command parsing.

Collect:

-   temperature.gpu
    
-   utilization.gpu
    
-   memory.used
    
-   memory.total
    

You don’t need NVML SDK for MVP.

Just parse stdout.

----------

## 2️⃣ XTTS Adapter (Simple Version)

Instead of deep integration:

You assume your XTTS script supports:

CONCURRENCY=4 python generate_xtts.py

Your Go tool:

-   Kills process
    
-   Restarts with new concurrency
    
-   Resumes from last checkpoint
    

You already built resumable pipeline before.

So MVP logic:

-   On adjustment → restart process with new concurrency.
    

Crude but effective.

----------

## 3️⃣ Throughput Measurement

Simplest method:

Monitor:

-   Output folder size growth  
    OR
    
-   Lines written to progress file  
    OR
    
-   Parse stdout for progress
    

You compute:

throughput = delta_units / delta_time

Baseline:

-   First 2 minutes average
    

----------

## 4️⃣ Config (Hardcoded or simple JSON)

{  
 "soft_temp": 78,  
 "hard_temp": 84,  
 "min_concurrency": 1,  
 "max_concurrency": 8,  
 "throughput_floor_ratio": 0.7,  
 "adjustment_cooldown_sec": 10  
}

Keep it simple.

----------

# 🕐 2-Day Implementation Plan

----------

## 🗓 Day 1 — Core Skeleton

### Morning

-   Setup CLI
    
-   Implement telemetry polling
    
-   Print real-time GPU stats
    

### Afternoon

-   Spawn XTTS process
    
-   Implement concurrency restart logic
    
-   Add simple logging
    

### Evening

-   Add throughput tracking
    
-   Establish baseline
    
-   Print live metrics
    

At end of Day 1:  
You can monitor + restart XTTS manually.

----------

## 🗓 Day 2 — Control Loop

### Morning

-   Implement control loop
    
-   Add concurrency adjustments
    
-   Add anti-oscillation cooldown
    

### Afternoon

-   Implement throughput floor logic
    
-   Add recovery behavior
    

### Evening

-   Test under:
    
    -   high concurrency
        
    -   forced overheating scenario
        
    -   long run
        

At end of Day 2:  
You have:

-   Automatic load regulator
    
-   Temperature guardrail
    
-   No 4–5× collapse
    
-   Logged decisions
    

----------

# 📊 What This MVP Proves

✅ You can regulate GPU workload  
✅ You can avoid catastrophic throttling  
✅ You can dynamically tune concurrency  
✅ You can build toward daemon architecture

----------

# 🚫 What This MVP Does NOT Include

-   No daemon API
    
-   No npm wrapper
    
-   No pip wrapper
    
-   No RL
    
-   No simulation
    
-   No calibration curves
    
-   No persistent session state
    
-   No web dashboard
    

Good.

Because MVP should be ruthless.

----------

# 🧠 Why This Is the Correct MVP

You’re testing one hypothesis:

> Can a simple rule-based controller prevent severe slowdown in long XTTS runs?

If yes:

-   You move to daemon architecture.
    
-   You formalize API.
    
-   You publish cross-ecosystem.
    

If no:

-   You rethink control strategy before building platform layers.
    

----------

# 🎯 Name It Something Temporary

Don’t overbrand yet.

Call it:

guardian-lite

Or

gsg-alpha

----------

# 🔥 Brutal Truth

You do NOT need:

-   Reinforcement learning
    
-   gRPC
    
-   npm packaging
    
-   pip packaging
    
-   Multi-GPU
    

To validate the core idea.

You need:

Telemetry + Throughput + Concurrency Control.

Everything else is future dopamine.