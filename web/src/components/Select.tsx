import {
  Children,
  isValidElement,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ComponentProps,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { cx } from "../styles/classes";
import { Icon } from "./Icon";

type Props = Pick<
  ComponentProps<"button">,
  | "id"
  | "className"
  | "disabled"
  | "aria-label"
  | "aria-labelledby"
  | "aria-describedby"
> & {
  children: ReactNode;
  value?: string;
  onChange: (value: string) => void;
  required?: boolean;
  name?: string;
};

function text(node: ReactNode): string {
  return Children.toArray(node)
    .map((child) =>
      isValidElement<{ children?: ReactNode }>(child)
        ? text(child.props.children)
        : String(child),
    )
    .join("");
}

export function Select({
  children,
  className = "",
  value,
  onChange,
  disabled,
  required,
  name,
  id,
  ...props
}: Props) {
  const options = Children.toArray(children)
    .filter(isValidElement<ComponentProps<"option">>)
    .map((option) => ({
      value: String(option.props.value ?? text(option.props.children)),
      label: text(option.props.children),
      disabled: option.props.disabled,
    }));
  const selected = options.findIndex(
    (option) => option.value === String(value),
  );
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const [invalid, setInvalid] = useState(false);
  const [position, setPosition] = useState({
    left: 0,
    top: 0,
    width: 0,
    maxHeight: 300,
  });
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const search = useRef({ value: "", at: 0 });
  const listID = useId();
  const first = options.findIndex((option) => !option.disabled);
  const show = () => {
    setActive(selected >= 0 && !options[selected].disabled ? selected : first);
    setOpen(true);
  };
  const choose = (index: number) => {
    if (!options[index] || options[index].disabled) return;
    onChange(options[index].value);
    setInvalid(false);
    setOpen(false);
    trigger.current?.focus();
  };
  useLayoutEffect(() => {
    if (!open || !trigger.current || !menu.current) return;
    const reposition = () => {
      if (!trigger.current) return;
      const rect = trigger.current.getBoundingClientRect();
      const below = window.innerHeight - rect.bottom - 16;
      const above = rect.top - 16;
      const height = Math.min(300, Math.max(below, above));
      const width = Math.min(Math.max(rect.width, 180), window.innerWidth - 24);
      setPosition({
        left: Math.max(12, Math.min(rect.left, window.innerWidth - width - 12)),
        top:
          below >= Math.min(180, above)
            ? rect.bottom + 6
            : Math.max(
                12,
                rect.top - Math.min(height, options.length * 38 + 12) - 6,
              ),
        width,
        maxHeight: height,
      });
    };
    reposition();
    menu.current.showPopover?.();
    const element = menu.current;
    window.addEventListener("resize", reposition);
    window.addEventListener("scroll", reposition, true);
    return () => {
      window.removeEventListener("resize", reposition);
      window.removeEventListener("scroll", reposition, true);
      if (element.hidePopover && element.matches(":popover-open"))
        element.hidePopover?.();
    };
  }, [open, options.length]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: PointerEvent) => {
      if (
        !menu.current?.contains(event.target as Node) &&
        !trigger.current?.contains(event.target as Node)
      )
        setOpen(false);
    };
    document.addEventListener("pointerdown", outside);
    return () => {
      document.removeEventListener("pointerdown", outside);
    };
  }, [open]);
  useEffect(() => {
    const list = menu.current;
    const option = list?.querySelector<HTMLElement>(
      `[data-index="${active}"]`,
    );
    if (!open || !list || !option) return;
    // Keep keyboard navigation inside the menu without scrolling its dialog.
    if (option.offsetTop < list.scrollTop) list.scrollTop = option.offsetTop;
    else if (
      option.offsetTop + option.offsetHeight > list.scrollTop + list.clientHeight
    )
      list.scrollTop = option.offsetTop + option.offsetHeight - list.clientHeight;
  }, [active, open]);
  return (
    <span className={cx("select-wrap")}>
      <button
        {...props}
        id={id}
        ref={trigger}
        type="button"
        value={value ?? ""}
        role="combobox"
        className={`${cx("select")} ${className}`}
        aria-expanded={open}
        aria-controls={open ? listID : undefined}
        aria-haspopup="listbox"
        aria-required={required || undefined}
        aria-invalid={invalid || undefined}
        aria-activedescendant={
          open && active >= 0 ? `${listID}-${active}` : undefined
        }
        disabled={disabled}
        onClick={() => (open ? setOpen(false) : show())}
        onKeyDown={(event) => {
          if (event.key === "Escape" && open) {
            event.preventDefault();
            event.stopPropagation();
            setOpen(false);
            return;
          }
          if (event.key === "Tab") {
            setOpen(false);
            return;
          }
          if (["Enter", " "].includes(event.key)) {
            event.preventDefault();
            if (open) choose(active);
            else show();
            return;
          }
          if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
            event.preventDefault();
            if (!open) {
              show();
              return;
            }
            const direction =
              event.key === "ArrowUp" || event.key === "End" ? -1 : 1;
            let index =
              event.key === "Home"
                ? -1
                : event.key === "End"
                  ? options.length
                  : active;
            for (let count = 0; count < options.length; count++) {
              index = (index + direction + options.length) % options.length;
              if (!options[index].disabled) {
                setActive(index);
                break;
              }
            }
          } else if (
            event.key.length === 1 &&
            !event.ctrlKey &&
            !event.metaKey &&
            !event.altKey
          ) {
            event.preventDefault();
            const query =
              (Date.now() - search.current.at < 700
                ? search.current.value
                : "") + event.key.toLocaleLowerCase();
            search.current = { value: query, at: Date.now() };
            const index = options.findIndex(
              (option) =>
                !option.disabled &&
                option.label.toLocaleLowerCase().startsWith(query),
            );
            if (!open) show();
            if (index >= 0) setActive(index);
          }
        }}
      >
        <span className={cx("select-value")}>
          {options[selected]?.label ?? "请选择"}
        </span>
        <Icon name="chevron" size={14} />
      </button>
      <select
        hidden
        aria-hidden="true"
        tabIndex={-1}
        name={name}
        value={value}
        required={required}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
        onInvalid={(event) => {
          event.preventDefault();
          setInvalid(true);
          trigger.current?.focus();
        }}
      >
        {children}
      </select>
      {open &&
        createPortal(
          <div
            ref={menu}
            id={listID}
            role="listbox"
            popover="manual"
            className={cx("select-menu")}
            aria-label={`${props["aria-label"] || "选择"}选项`}
            style={{ ...position, display: "block", position: "fixed" }}
          >
            {options.map((option, index) => (
              <div
                key={option.value}
                id={`${listID}-${index}`}
                role="option"
                aria-selected={selected === index}
                aria-disabled={option.disabled || undefined}
                data-value={option.value}
                data-index={index}
                className={cx(
                  `select-option ${index === active ? "highlighted" : ""}`,
                )}
                onPointerDown={(event) => event.preventDefault()}
                onPointerMove={() => {
                  if (!option.disabled) setActive(index);
                }}
                onClick={(event) => {
                  event.preventDefault();
                  choose(index);
                }}
              >
                <span>{option.label}</span>
                {selected === index && <span aria-hidden="true">✓</span>}
              </div>
            ))}
          </div>,
          trigger.current?.closest("dialog") ?? document.body,
        )}
    </span>
  );
}
