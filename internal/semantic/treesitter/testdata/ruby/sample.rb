require "json"

module Billing
  TAX_RATE = 0.2

  class Invoice < Base
    attr_reader :lines

    def initialize(lines)
      @lines = lines
    end

    def total
      lines.sum(&:amount)
    end

    def paid?
      !@paid_at.nil?
    end

    def self.build(attrs)
      new(attrs.fetch(:lines))
    end

    class << self
      def from_json(text)
        build(JSON.parse(text))
      end
    end

    private def audit
      log(total)
    end
  end

  module Formatting
    def format_amount(cents)
      format("%.2f", cents / 100.0)
    end
  end
end

class Billing::Refund
  def ==(other)
    other.is_a?(Refund)
  end
end

def helper(x) = x * 2

def main
  Billing::Invoice.build(lines: []).total
end
