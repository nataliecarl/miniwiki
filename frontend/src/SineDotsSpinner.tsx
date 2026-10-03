import { useEffect, useMemo, useState } from "react";

type SineDotsSpinnerProps = {
  color?: string;
  pointCount?: number;
  dotSize?: number;
  spacing?: number;
  amplitude?: number;
  timeSpeed?: number;
  frequency?: number;
  className?: string;
};

export function SineDotsSpinner({
  color = "currentColor",
  pointCount = 12,
  dotSize = 4,
  spacing = 2,
  amplitude = 12,
  timeSpeed = 1.0,
  frequency = 1.05,
  className
}: SineDotsSpinnerProps) {
  const [time, setTime] = useState(0);

  useEffect(() => {
    let rafId = 0;
    const start = performance.now();
    const frame = (now: number) => {
      const elapsed = (now - start) / 1000;
      setTime(elapsed * timeSpeed);
      rafId = requestAnimationFrame(frame);
    };
    rafId = requestAnimationFrame(frame);
    return () => cancelAnimationFrame(rafId);
  }, [timeSpeed]);

  const dots = useMemo(() => Array.from({ length: pointCount }, (_, i) => i), [pointCount]);
  const height = dotSize + amplitude * 2;

  return (
    <span
      aria-label="Loading"
      role="status"
      className={className}
      style={{ display: "inline-flex", alignItems: "center", height }}
    >
      <span style={{ display: "inline-flex", alignItems: "center", gap: spacing }}>
        {dots.map((index) => {
          const multiplier = frequency * (index + 1);
          const y = Math.sin(time * multiplier) * amplitude;
          return (
            <span
              key={index}
              style={{
                width: dotSize,
                height: dotSize,
                backgroundColor: color,
                borderRadius: 9999,
                transform: `translateY(${-y}px)`
              }}
            />
          );
        })}
      </span>
    </span>
  );
}
