import React, { useState } from 'react';

interface ButtonProps {
    label: string;
    onClick: () => void;
}

type Variant = 'primary' | 'secondary';

export function Button({ label, onClick }: ButtonProps) {
    return (
        <button className="btn" onClick={onClick}>
            {label}
        </button>
    );
}

export const Counter = ({ start }: { start: number }) => {
    const [count, setCount] = useState(start);
    return <span onClick={() => setCount(count + 1)}>{count}</span>;
};

export function List<T>({ items, render }: { items: T[]; render: (item: T) => JSX.Element }) {
    return <ul>{items.map((item, i) => <li key={i}>{render(item)}</li>)}</ul>;
}

export default class Panel extends React.Component<{ title: string }> {
    render() {
        return <section><h1>{this.props.title}</h1></section>;
    }
}
