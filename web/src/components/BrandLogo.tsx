export function BrandLogo({
  size = 24,
  className,
}: {
  size?: number;
  className?: string;
}) {
  return (
    <img
      src="/favicon.svg"
      width={size}
      height={size}
      alt=""
      draggable={false}
      className={className}
    />
  );
}
