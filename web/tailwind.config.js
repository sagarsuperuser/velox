/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: ['./index.html', './src/**/*.{js,ts,jsx,tsx}'],
  theme: {
    extend: {
      fontFamily: {
        sans: ['Inter', 'system-ui', '-apple-system', 'sans-serif'],
      },
      colors: {
        velox: {
          50: '#F7F7FF',
          100: '#EDEDFC',
          500: '#635BFF',
          600: '#5851EB',
          700: '#4B45D1',
          900: '#1A1523',
        },
      },
      boxShadow: {
        card: '0 1px 3px 0 rgb(0 0 0 / 0.04), 0 1px 2px -1px rgb(0 0 0 / 0.03)',
        'card-hover': '0 4px 6px -1px rgb(0 0 0 / 0.06), 0 2px 4px -2px rgb(0 0 0 / 0.04)',
        modal: '0 25px 50px -12px rgb(0 0 0 / 0.15)',
        toast: '0 4px 12px rgb(0 0 0 / 0.08), 0 0 1px rgb(0 0 0 / 0.06)',
      },
    },
  },
  plugins: [],
}
