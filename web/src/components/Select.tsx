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
import { scoped } from "../styles/classes";
import controls from "../styles/controls.module.css";
import layout from "../styles/layout.module.css";
import conversation from "../styles/conversation.module.css";
import panels from "../styles/panels.module.css";
import { Icon } from "./Icon";
import { t, useLanguage } from "../lib/i18n";
import { revealOption, useAnchoredOverlay } from "../lib/overlay";

const cx = scoped(controls, layout, conversation, panels);

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
  containerClassName?: string;
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
  containerClassName,
  value,
  onChange,
  disabled,
  required,
  name,
  id,
  ...props
}: Props) {
  useLanguage();
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
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const search = useRef({ value: "", at: 0 });
  const listID = useId();
  const first = options.findIndex((option) => !option.disabled);
  const show = () => {
    if (first < 0) return;
    search.current = { value: "", at: 0 };
    setActive(selected >= 0 && !options[selected].disabled ? selected : first);
    setOpen(true);
  };
  const choose = (index: number) => {
    if (!options[index] || options[index].disabled) return;
    onChange(options[index].value);
    setInvalid(false);
    setOpen(false);
    trigger.current?.focus({ preventScroll: true });
  };
  useLayoutEffect(() => {
    if (!open || !menu.current) return;
    const element = menu.current;
    element.showPopover?.();
    return () => {
      if (element.hidePopover && element.matches(":popover-open"))
        element.hidePopover();
    };
  }, [open]);
  useAnchoredOverlay(open, trigger, menu);
  useEffect(() => {
    if (!open) return;
    const outside = (event: Event) => {
      if (
        !menu.current?.contains(event.target as Node) &&
        !trigger.current?.contains(event.target as Node)
      )
        setOpen(false);
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("focusin", outside);
    };
  }, [open]);
  useEffect(() => {
    const list = menu.current;
    const option = list?.querySelector<HTMLElement>(`[data-index="${active}"]`);
    if (!open || !list || !option) return;
    revealOption(list, option);
  }, [active, open]);
  return (
    <span
      className={[cx("select-wrap"), containerClassName]
        .filter(Boolean)
        .join(" ")}
    >
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
          if (event.nativeEvent.isComposing || event.keyCode === 229) return;
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
            if (!open) show();
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
            if (index >= 0) setActive(index);
          }
        }}
      >
        <span className={cx("select-value")}>
          {options[selected]?.label ?? t("请选择", "Select")}
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
            aria-label={props["aria-label"] || t("选项", "Options")}
            style={{ display: "block" }}
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
                onPointerDown={(event) => {
                  if (event.pointerType === "mouse") event.preventDefault();
                }}
                onPointerMove={(event) => {
                  if (event.pointerType === "mouse" && !option.disabled)
                    setActive(index);
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
          document.body,
        )}
    </span>
  );
}
